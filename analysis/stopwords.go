package analysis

// englishStopWords is the Snowball English stop-word list
// (https://snowballstem.org/algorithms/english/stop.txt), the same list
// Lucene and Elasticsearch use for the _english_ set. It is embedded as a Go
// slice so the binary stays dependency-free and the stop filter is reproducible.
var englishStopWords = []string{
	"i", "me", "my", "myself", "we", "our", "ours", "ourselves",
	"you", "your", "yours", "yourself", "yourselves",
	"he", "him", "his", "himself", "she", "her", "hers", "herself",
	"it", "its", "itself", "they", "them", "their", "theirs", "themselves",
	"what", "which", "who", "whom", "this", "that", "these", "those",
	"am", "is", "are", "was", "were", "be", "been", "being",
	"have", "has", "had", "having", "do", "does", "did", "doing",
	"would", "should", "could", "ought",
	"i'm", "you're", "he's", "she's", "it's", "we're", "they're",
	"i've", "you've", "we've", "they've",
	"i'd", "you'd", "he'd", "she'd", "we'd", "they'd",
	"i'll", "you'll", "he'll", "she'll", "we'll", "they'll",
	"isn't", "aren't", "wasn't", "weren't", "hasn't", "haven't", "hadn't",
	"doesn't", "don't", "didn't", "won't", "wouldn't", "shan't", "shouldn't",
	"can't", "cannot", "couldn't", "mustn't",
	"let's", "that's", "who's", "what's", "here's", "there's", "when's",
	"where's", "why's", "how's",
	"a", "an", "the", "and", "but", "if", "or", "because", "as", "until",
	"while", "of", "at", "by", "for", "with", "about", "against", "between",
	"into", "through", "during", "before", "after", "above", "below",
	"to", "from", "up", "down", "in", "out", "on", "off", "over", "under",
	"again", "further", "then", "once",
	"here", "there", "when", "where", "why", "how",
	"all", "any", "both", "each", "few", "more", "most", "other", "some",
	"such", "no", "nor", "not", "only", "own", "same", "so", "than", "too",
	"very",
}

// stopSet builds a lookup set from a word list.
func stopSet(words []string) map[string]struct{} {
	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}

// englishStopSet is the prebuilt set for the _english_ predefined list.
var englishStopSet = stopSet(englishStopWords)
