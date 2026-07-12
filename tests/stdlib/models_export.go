package stdlibcorpus

// Exported aliases let external benchmark modules use the exact concrete
// models without maintaining a second copy of the Go standard library corpus.
type (
	CanadaRoot  = canadaRoot
	CITMRoot    = citmRoot
	GolangRoot  = golangRoot
	StringRoot  = stringRoot
	SyntheaRoot = syntheaRoot
	TwitterRoot = twitterRoot
)
