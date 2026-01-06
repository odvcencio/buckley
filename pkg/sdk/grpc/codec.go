package grpcsdk

import (
	"encoding/json"

	"google.golang.org/grpc/encoding"
)

// jsonCodec is a lightweight codec so we can serve gRPC without proto generation yet.
type jsonCodec struct{}

func (jsonCodec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func (jsonCodec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func (jsonCodec) Name() string {
	return "json"
}

func init() {
	encoding.RegisterCodec(jsonCodec{})
}
