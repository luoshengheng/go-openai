package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	utils "github.com/sashabaranov/go-openai/internal"
)

var (
	headerData  = []byte("data: ")
	errorPrefix = []byte(`data: {"error":`)
)

type Streamable interface {
	ChatCompletionStreamResponse | CompletionResponse
}

type StreamReader[T Streamable] struct {
	EmptyMessagesLimit uint
	IsFinished         bool
	ResponsePlainText  bool
	Reader             *bufio.Reader
	Response           *http.Response
	ErrAccumulator     utils.ErrorAccumulator
	Unmarshaler        utils.Unmarshaler
}

func (stream *StreamReader[T]) Recv() (response T, err error) {
	if stream.IsFinished {
		err = io.EOF
		return
	}

	response, err = stream.processLines()
	return
}

//nolint:gocognit
func (stream *StreamReader[T]) processLines() (T, error) {
	var (
		emptyMessagesCount uint
		hasErrorPrefix     bool
	)
	if stream.ResponsePlainText {
		totalBytes := []byte{}
		for {
			respBytes := make([]byte, 1)
			_, readErr := stream.Reader.Read(respBytes)
			if readErr != nil {
				respErr := stream.unmarshalError()
				if respErr != nil {
					return *new(T), fmt.Errorf("error, %w", respErr.Error)
				}
				return *new(T), readErr
			}
			totalBytes = append(totalBytes, respBytes...)
			r, _ := utf8.DecodeRune(totalBytes)
			if r == utf8.RuneError {
				continue
			}

			var response ChatCompletionStreamResponse
			response.Choices = []ChatCompletionStreamChoice{{Index: 0, Delta: ChatCompletionStreamChoiceDelta{Content: string(totalBytes)}, FinishReason: "PlainText"}}
			bytes, _ := json.Marshal(response)

			var t T
			unmarshalErr := stream.Unmarshaler.Unmarshal(bytes, &t)
			if unmarshalErr != nil {
				return *new(T), unmarshalErr
			}
			return t, nil

		}
	} else {
		for {
			rawLine, readErr := stream.Reader.ReadBytes('\n')
			if readErr != nil || hasErrorPrefix {
				respErr := stream.unmarshalError()
				if respErr != nil {
					return *new(T), fmt.Errorf("error, %w", respErr.Error)
				}
				return *new(T), readErr
			}

			noSpaceLine := bytes.TrimSpace(rawLine)
			if bytes.HasPrefix(noSpaceLine, errorPrefix) {
				hasErrorPrefix = true
			}
			if !bytes.HasPrefix(noSpaceLine, headerData) || hasErrorPrefix {
				if hasErrorPrefix {
					noSpaceLine = bytes.TrimPrefix(noSpaceLine, headerData)
				}
				writeErr := stream.ErrAccumulator.Write(noSpaceLine)
				if writeErr != nil {
					return *new(T), writeErr
				}
				emptyMessagesCount++
				if emptyMessagesCount > stream.EmptyMessagesLimit {
					return *new(T), ErrTooManyEmptyStreamMessages
				}

				continue
			}

			noPrefixLine := bytes.TrimPrefix(noSpaceLine, headerData)
			if string(noPrefixLine) == "[DONE]" {
				stream.IsFinished = true
				return *new(T), io.EOF
			}

			var response T
			unmarshalErr := stream.Unmarshaler.Unmarshal(noPrefixLine, &response)
			if unmarshalErr != nil {
				return *new(T), unmarshalErr
			}
			return response, nil
		}
	}
}

func (stream *StreamReader[T]) unmarshalError() (errResp *ErrorResponse) {
	errBytes := stream.ErrAccumulator.Bytes()
	if len(errBytes) == 0 {
		return
	}

	err := stream.Unmarshaler.Unmarshal(errBytes, &errResp)
	if err != nil {
		errResp = nil
	}

	return
}

func (stream *StreamReader[T]) Close() {
	stream.Response.Body.Close()
}
