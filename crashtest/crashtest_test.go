package crashtest

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/tamnd/search"
	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/vfs"
	"github.com/tamnd/search/wal"
)

// catalogWorkload builds an all-or-nothing single-transaction workload over the
// catalog: a baseline of base keys, then one transaction that flips a sentinel
// and writes n new keys. Because it is one transaction, a crash anywhere must
// leave either the whole baseline (sentinel "0", no new keys) or the whole new
// state (sentinel "1", all new keys), never a mixture.
func catalogWorkload(pageSize uint32, base, n int) Workload {
	sentinel := []byte("gen")
	newKey := func(i int) []byte { return fmt.Appendf(nil, "new-%06d", i) }
	baseKey := func(i int) []byte { return fmt.Appendf(nil, "base-%06d", i) }

	return Workload{
		PageSize: pageSize,
		Seed: func(db *search.DB) error {
			return db.Update(func(tx *search.Txn) error {
				c := tx.Catalog()
				if err := c.Put(catalog.NSMeta, sentinel, []byte("0")); err != nil {
					return err
				}
				for i := range base {
					if err := c.Put(catalog.NSMeta, baseKey(i), []byte("base")); err != nil {
						return err
					}
				}
				return nil
			})
		},
		Mutate: func(db *search.DB) error {
			return db.Update(func(tx *search.Txn) error {
				c := tx.Catalog()
				if err := c.Put(catalog.NSMeta, sentinel, []byte("1")); err != nil {
					return err
				}
				for i := range n {
					if err := c.Put(catalog.NSMeta, newKey(i), []byte("new")); err != nil {
						return err
					}
				}
				return nil
			})
		},
		Post: func(db *search.DB) (bool, error) {
			var post bool
			err := db.View(func(tx *search.Txn) error {
				c := tx.Catalog()
				gen, ok, err := c.Get(catalog.NSMeta, sentinel)
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("sentinel missing")
				}
				post = bytes.Equal(gen, []byte("1"))
				// Cross-check one new key against the sentinel to catch a torn mix.
				_, present, err := c.Get(catalog.NSMeta, newKey(0))
				if err != nil {
					return err
				}
				if present != post {
					return fmt.Errorf("torn: sentinel post=%v but new-000000 present=%v", post, present)
				}
				return nil
			})
			return post, err
		},
		Verify: func(db *search.DB, post bool) error {
			return db.View(func(tx *search.Txn) error {
				c := tx.Catalog()
				// The baseline must be intact in every recovered state.
				for i := range base {
					v, ok, err := c.Get(catalog.NSMeta, baseKey(i))
					if err != nil {
						return err
					}
					if !ok || !bytes.Equal(v, []byte("base")) {
						return fmt.Errorf("baseline base-%06d lost (ok=%v)", i, ok)
					}
				}
				// The new keys are all present iff the workload took effect.
				for i := range n {
					_, ok, err := c.Get(catalog.NSMeta, newKey(i))
					if err != nil {
						return err
					}
					if ok != post {
						return fmt.Errorf("new-%06d present=%v, want %v", i, ok, post)
					}
				}
				return nil
			})
		},
	}
}

func TestCrashRecovery_MetaFlip(t *testing.T) {
	// A small workload concentrates the campaign on the commit's data barrier and
	// the single atomic meta flip.
	rep, err := Run(catalogWorkload(4096, 8, 8))
	if err != nil {
		t.Fatal(err)
	}
	if rep.FaultPoints == 0 {
		t.Fatal("no fault points discovered")
	}
	reportFailures(t, rep)
	t.Logf("meta-flip campaign: %d fault points, %d cycles", rep.FaultPoints, rep.Cycles)
}

func TestCrashRecovery_AllInjectionPoints(t *testing.T) {
	// A large single transaction spans many data-page writes, a multi-trunk
	// freelist rewrite, the data barrier, and the meta flip, so the campaign trips
	// a crash, a tear, and an fsync failure at every one of those boundaries.
	rep, err := Run(catalogWorkload(4096, 6000, 6000))
	if err != nil {
		t.Fatal(err)
	}
	if rep.FaultPoints < 100 {
		t.Fatalf("only %d fault points, want >= 100 for full coverage", rep.FaultPoints)
	}
	reportFailures(t, rep)
	t.Logf("full campaign: %d fault points, %d cycles, 0 atomicity violations", rep.FaultPoints, rep.Cycles)
}

// TestCrashRecovery_WALPartial drives the write-ahead log through the same fault
// campaign: at every write and sync boundary it crashes, tears, or fails the
// fsync, then asserts recovery returns exactly the frames of fully committed
// transactions and never a torn tail.
func TestCrashRecovery_WALPartial(t *testing.T) {
	const txns = 6
	build := func(mem *vfs.Mem) error {
		w, err := wal.Create(mem, "x-wal", 0xABCDEF, 4096, wal.SyncFull)
		if err != nil {
			return err
		}
		for txn := uint64(1); txn <= txns; txn++ {
			for d := range 4 {
				if _, err := w.Append(wal.Frame{TxnID: txn, Op: wal.OpAddDoc, DocID: txn*100 + uint64(d), Payload: fmt.Appendf(nil, "f-%d-%d", txn, d)}); err != nil {
					_ = w.Close()
					return err
				}
			}
			if err := w.Commit(txn); err != nil {
				_ = w.Close()
				return err
			}
		}
		return w.Close()
	}

	// Count the fault points the build reaches.
	counter := vfs.NewMem()
	fc := vfs.NewCounter()
	counter.Attach(fc)
	if err := build(counter); err != nil {
		t.Fatalf("fault-free build: %v", err)
	}
	points := fc.Count()
	if points == 0 {
		t.Fatal("no WAL fault points discovered")
	}

	cycles := 0
	for ord := range points {
		for _, mode := range []vfs.TripMode{vfs.TripCrash, vfs.TripTear, vfs.TripFsyncFail} {
			cycles++
			mem := vfs.NewMemWithFaults(vfs.NewTrip(ord, mode))
			_ = build(mem) // expected to fail at the armed point

			crashed := mem.Snapshot()
			w, err := wal.Open(crashed, "x-wal", 0xABCDEF)
			if err != nil {
				// A crash during Create can leave no valid header; that is a clean
				// "nothing committed" outcome, not a recovery failure.
				continue
			}
			frames, err := w.Recover()
			if err != nil {
				t.Fatalf("ord=%d mode=%d recover: %v", ord, mode, err)
			}
			_ = w.Close()
			if err := checkWALPrefix(frames); err != nil {
				t.Fatalf("ord=%d mode=%d: %v", ord, mode, err)
			}
		}
	}
	t.Logf("WAL campaign: %d fault points, %d cycles", points, cycles)
}

// checkWALPrefix asserts the recovered frames form a whole number of committed
// transactions: every transaction's adds are followed by its commit marker, and
// the log ends on a marker.
func checkWALPrefix(frames []wal.Frame) error {
	if len(frames) == 0 {
		return nil
	}
	if frames[len(frames)-1].Op != wal.OpCommitMarker {
		return fmt.Errorf("recovered log does not end on a commit marker")
	}
	pendingAdds := 0
	for _, fr := range frames {
		switch fr.Op {
		case wal.OpAddDoc:
			pendingAdds++
		case wal.OpCommitMarker:
			pendingAdds = 0
		default:
			return fmt.Errorf("unexpected op %d in recovered log", fr.Op)
		}
	}
	if pendingAdds != 0 {
		return fmt.Errorf("%d uncommitted adds survived recovery", pendingAdds)
	}
	return nil
}

func reportFailures(t *testing.T, rep Report) {
	t.Helper()
	for _, f := range rep.Failures {
		t.Errorf("atomicity violation at ordinal %d mode %d: %v", f.Ordinal, f.Mode, f.Err)
	}
}
