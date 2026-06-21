package docvalues

import "math"

// Geo quantization constants (doc 14 §9.1). Latitude spans 180 degrees, longitude
// 360, each quantized into 32 bits and interleaved into a 64-bit Morton code.
const (
	latScale = float64(uint64(1)<<32) / 180.0
	lonScale = float64(uint64(1)<<32) / 360.0
)

// EncodeLatLon returns the 64-bit Morton code for a latitude/longitude pair.
func EncodeLatLon(lat, lon float64) uint64 {
	latQ := uint32((lat + 90.0) * latScale)
	lonQ := uint32((lon + 180.0) * lonScale)
	return spreadBits(uint64(latQ)) | (spreadBits(uint64(lonQ)) << 1)
}

// DecodeLatLon reverses EncodeLatLon.
func DecodeLatLon(morton uint64) (lat, lon float64) {
	latQ := compactBits(morton)
	lonQ := compactBits(morton >> 1)
	lat = float64(latQ)/latScale - 90.0
	lon = float64(lonQ)/lonScale - 180.0
	return
}

// spreadBits spreads the low 32 bits of v into the even bit positions.
func spreadBits(v uint64) uint64 {
	v &= 0x00000000FFFFFFFF
	v = (v | (v << 16)) & 0x0000FFFF0000FFFF
	v = (v | (v << 8)) & 0x00FF00FF00FF00FF
	v = (v | (v << 4)) & 0x0F0F0F0F0F0F0F0F
	v = (v | (v << 2)) & 0x3333333333333333
	v = (v | (v << 1)) & 0x5555555555555555
	return v
}

// compactBits gathers the even bit positions of v back into the low 32 bits.
func compactBits(v uint64) uint32 {
	v &= 0x5555555555555555
	v = (v | (v >> 1)) & 0x3333333333333333
	v = (v | (v >> 2)) & 0x0F0F0F0F0F0F0F0F
	v = (v | (v >> 4)) & 0x00FF00FF00FF00FF
	v = (v | (v >> 8)) & 0x0000FFFF0000FFFF
	v = (v | (v >> 16)) & 0x00000000FFFFFFFF
	return uint32(v)
}

// GeoWriter accumulates one geographic point per document and serializes a
// GEO_POINT column, which is a NUMERIC column over Morton codes.
type GeoWriter struct {
	num *NumericWriter
}

// NewGeoWriter returns a writer for docCount documents.
func NewGeoWriter(docCount uint32) *GeoWriter {
	return &GeoWriter{num: NewNumericWriter(docCount, false)}
}

// Set records a lat/lon point for doc index i.
func (w *GeoWriter) Set(i uint32, lat, lon float64) {
	w.num.Set(i, int64(EncodeLatLon(lat, lon)))
}

// Bytes serializes the column with the GEO_POINT kind tag.
func (w *GeoWriter) Bytes() []byte {
	b := w.num.Bytes()
	b[0] = byte(KindGeoPoint)
	return b
}

// geoColumn is the read side of a GEO_POINT column, wrapping a numericColumn.
type geoColumn struct{ *numericColumn }

func openGeo(blob []byte) (*geoColumn, error) {
	n, err := openNumeric(blob)
	if err != nil {
		return nil, err
	}
	return &geoColumn{numericColumn: n}, nil
}

// Kind reports the column's structural kind, KindGeoPoint.
func (c *geoColumn) Kind() ColumnKind { return KindGeoPoint }

// Morton returns the raw 64-bit Morton code stored for doc index i.
func (c *geoColumn) Morton(i uint32) uint64 { return uint64(c.Int64(i)) }

// LatLon returns the latitude and longitude decoded from doc index i's Morton code.
func (c *geoColumn) LatLon(i uint32) (lat, lon float64) {
	return DecodeLatLon(c.Morton(i))
}

// Haversine returns the great-circle distance in meters between two points
// (doc 14 §9.2). The query layer uses it for geo-distance sorting and geo
// aggregations.
func Haversine(lat1, lon1, lat2, lon2 float64) float64 {
	return haversine(lat1, lon1, lat2, lon2)
}

// haversine returns the great-circle distance in meters between two points
// (doc 14 §9.2).
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	dlat := (lat2 - lat1) * math.Pi / 180.0
	dlon := (lon2 - lon1) * math.Pi / 180.0
	a := math.Sin(dlat/2)*math.Sin(dlat/2) +
		math.Cos(lat1*math.Pi/180.0)*math.Cos(lat2*math.Pi/180.0)*
			math.Sin(dlon/2)*math.Sin(dlon/2)
	return r * 2.0 * math.Atan2(math.Sqrt(a), math.Sqrt(1.0-a))
}
