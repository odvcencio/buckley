package headless

import (
	"context"
	"fmt"

	"github.com/odvcencio/buckley/pkg/model"
)

type headlessModelClient struct {
	runner *Runner
}

func (c *headlessModelClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if c == nil || c.runner == nil {
		return nil, fmt.Errorf("runner not available")
	}

	resp, err := c.runner.callModel(ctx, req)
	if err != nil || resp == nil {
		return resp, err
	}
	if len(req.Tools) == 0 {
		return resp, err
	}
	if len(resp.Choices) == 0 {
		return resp, err
	}

	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) > 0 {
		c.runner.conv.AddToolCallMessage(msg.ToolCalls)
	}

	return resp, err
}

func (c *headlessModelClient) GetExecutionModel() string {
	if c == nil || c.runner == nil || c.runner.modelManager == nil {
		return ""
	}
	return c.runner.modelManager.GetExecutionModel()
}

func (c *headlessModelClient) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	if c == nil || c.runner == nil || c.runner.modelManager == nil {
		errChan := make(chan error, 1)
		errChan <- fmt.Errorf("runner not available")
		close(errChan)
		return nil, errChan
	}
	return c.runner.modelManager.ChatCompletionStream(ctx, req)
}

type toolExecutionError struct {
	err error
}

func (e toolExecutionError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e toolExecutionError) Unwrap() error {
	return e.err
}
