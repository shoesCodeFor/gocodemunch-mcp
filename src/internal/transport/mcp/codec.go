package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func readRPCMessage(reader *bufio.Reader) (rpcRequest, error) {
	contentLength, err := readContentLength(reader)
	if err != nil {
		return rpcRequest{}, err
	}

	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return rpcRequest{}, err
	}

	var request rpcRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return rpcRequest{}, fmt.Errorf("decode request: %w", err)
	}
	if request.JSONRPC == "" {
		request.JSONRPC = "2.0"
	}
	return request, nil
}

func writeRPCMessage(writer *bufio.Writer, response rpcResponse) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode response: %w", err)
	}

	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := writer.Write(payload); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush payload: %w", err)
	}
	return nil
}

func readContentLength(reader *bufio.Reader) (int, error) {
	contentLength := -1
	readAnyHeader := false

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && !readAnyHeader {
				return 0, io.EOF
			}
			return 0, err
		}

		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		readAnyHeader = true

		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "Content-Length") {
			continue
		}

		value := strings.TrimSpace(parts[1])
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("invalid Content-Length %q: %w", value, err)
		}
		if parsed < 0 {
			return 0, fmt.Errorf("negative Content-Length: %d", parsed)
		}
		contentLength = parsed
	}

	if !readAnyHeader && contentLength == -1 {
		return 0, io.EOF
	}
	if contentLength == -1 {
		return 0, errors.New("missing Content-Length header")
	}
	return contentLength, nil
}
