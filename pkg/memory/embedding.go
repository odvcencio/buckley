package memory

// SerializeEmbedding exposes the memory embedding serialization format.
func SerializeEmbedding(embedding []float64) ([]byte, error) {
	return serializeEmbedding(embedding)
}

// DeserializeEmbedding exposes the memory embedding decoding format.
func DeserializeEmbedding(data []byte) ([]float64, error) {
	return deserializeEmbedding(data)
}
