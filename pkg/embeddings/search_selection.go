package embeddings

// searchCandidates keeps the weakest retained match at the heap root.
type searchCandidates SearchResults

func (r searchCandidates) Len() int           { return len(r) }
func (r searchCandidates) Less(i, j int) bool { return r[i].Similarity < r[j].Similarity }
func (r searchCandidates) Swap(i, j int)      { r[i], r[j] = r[j], r[i] }

func (r *searchCandidates) Push(value any) {
	*r = append(*r, value.(SearchResult))
}

func (r *searchCandidates) Pop() any {
	last := len(*r) - 1
	value := (*r)[last]
	(*r)[last] = SearchResult{}
	*r = (*r)[:last]
	return value
}
