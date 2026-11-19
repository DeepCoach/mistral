package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// directHeaderTimeout bounds how long a direct request may wait for response
// headers. Mistral's gateway only gives up on a stuck backend after 15 minutes
// and answers 504, which is far longer than any client waits, so the plugin
// fails fast and retries instead.
//
// It is deliberately generous. Headers only arrive once the model starts
// producing output, so this is the budget for the first token, not for a
// round-trip: at the high effort settings a long prompt legitimately says
// nothing for minutes. The upstream Vibe client waits 720s for the same thing.
// A value far below that aborts healthy requests, and each abort costs the
// whole generation.
const directHeaderTimeout = 600 * time.Second

var (
	directTransportOnce sync.Once
	directTransportPool *http.Transport

	directStreamTransportOnce sync.Once
	directStreamTransportPool *http.Transport
)

// directTransport bypasses every proxy, including the process environment.
// ignore-proxy exists so this provider can opt out of the CPA global proxy,
// which the host callbacks would otherwise apply to every upstream request.
// One shared transport keeps upstream connections reusable across requests.
func directTransport() *http.Transport {
	directTransportOnce.Do(func() {
		if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
			clone := transport.Clone()
			clone.Proxy = nil
			directTransportPool = clone
			return
		}
		directTransportPool = &http.Transport{Proxy: nil}
	})
	return directTransportPool
}

// directStreamTransport is the streaming variant: it keeps the direct dialing
// but bounds the wait for response headers. The body itself is never subject to
// this timeout, so long generations still stream to completion.
func directStreamTransport() *http.Transport {
	directStreamTransportOnce.Do(func() {
		clone := directTransport().Clone()
		clone.ResponseHeaderTimeout = directHeaderTimeout
		directStreamTransportPool = clone
	})
	return directStreamTransportPool
}

// directStreamOpen dials Mistral directly and returns once response headers
// arrive; the caller reads the body.
func directStreamOpen(url string, headers map[string][]string, body []byte) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if request.Header.Get("User-Agent") == "" {
		request.Header.Set("User-Agent", "kaecho-mistral/"+pluginVersion)
	}
	// Keep the connection open for streaming; only the wait for response
	// headers is bounded, the idle cutoff is enforced by pumpDirectStream.
	client := &http.Client{Transport: directStreamTransport()}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("direct upstream request: %w", err)
	}
	return response, nil
}

// pumpDirectStream forwards a direct upstream SSE body to the host stream and
// records the same request log entry the host path produces.
func pumpDirectStream(streamID string, resp *http.Response, started time.Time, entry requestLog, idle int) {
	defer func() {
		_ = resp.Body.Close()
		_, _ = callHost("host.stream.close", map[string]any{"stream_id": streamID})
	}()
	var idleTimer *time.Timer
	if idle > 0 {
		idleTimer = time.AfterFunc(time.Duration(idle)*time.Second, func() {
			_ = resp.Body.Close()
		})
		defer idleTimer.Stop()
	}
	resetIdle := func() {
		if idleTimer != nil {
			idleTimer.Reset(time.Duration(idle) * time.Second)
		}
	}
	// The plugin RPC caller only starts reading host stream emissions after
	// streamUpstream returns, so give the caller a moment before the first emit.
	time.Sleep(50 * time.Millisecond)
	reader := bufio.NewReaderSize(resp.Body, 32*1024)
	buf := make([]byte, 32*1024)
	var pending []byte
	produced := false
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			// Only model output resets the idle limit. Mistral generates a tool
			// call whole and sends it at the end, so the bytes before it are
			// keep-alive comments that say nothing about whether the stream is
			// alive; counting them let a stalled stream hide behind heartbeats.
			// Until output starts, the wait is governed by the response-header
			// timeout, which is the right limit for a long first token.
			if produced {
				resetIdle()
			}
			pending = append(pending, buf[:n]...)
			if len(pending) > maxPendingSSEBytes {
				errNoFrame := fmt.Errorf("upstream sent no SSE frame separator within %d bytes", maxPendingSSEBytes)
				entry.Status = http.StatusBadGateway
				entry.Error = errNoFrame.Error()
				entry.DurationMs = time.Since(started).Milliseconds()
				appendRequestLog(entry)
				failStream(streamID, errNoFrame)
				return
			}
			frames, rest := splitSSE(pending)
			pending = rest
			for _, frame := range frames {
				if entry.TTFBMs == 0 {
					entry.TTFBMs = time.Since(started).Milliseconds()
				}
				captureFrameUsage(&entry, frame)
				if !produced && frameCarriesOutput(frame) {
					produced = true
					resetIdle()
				}
				if emitErr := emitFrame(streamID, frame); emitErr != nil {
					entry.Status = http.StatusBadGateway
					entry.Error = emitErr.Error()
					entry.DurationMs = time.Since(started).Milliseconds()
					appendRequestLog(entry)
					// The client is gone or the host stream is broken; emitting
					// the failure event would only fail again.
					return
				}
			}
		}
		if err == nil {
			continue
		}
		if len(bytes.TrimSpace(pending)) > 0 {
			captureFrameUsage(&entry, pending)
			_ = emitFrame(streamID, pending)
		}
		entry.DurationMs = time.Since(started).Milliseconds()
		if entry.TTFBMs == 0 {
			entry.TTFBMs = entry.DurationMs
		}
		if err == io.EOF {
			entry.Status = http.StatusOK
			entry.applyUsage(usageInfo{Prompt: entry.PromptTokens, Completion: entry.CompletionTokens, Cached: entry.CachedTokens}, entry.DurationMs)
			appendRequestLog(entry)
			return
		}
		entry.Status = http.StatusBadGateway
		entry.Error = err.Error()
		appendRequestLog(entry)
		// A connection that dropped mid-stream is the upstream's failure, not
		// the credential's, so it does not cool the key; but the client still
		// has to be told the stream is over instead of waiting forever.
		failStream(streamID, err)
		return
	}
}

// frameCarriesOutput reports whether an SSE frame holds model output rather
// than a preamble. The first frames usually carry only the assistant role and
// empty content, and Mistral's heartbeats are comment lines; neither means the
// model has started answering. This mirrors the upstream Vibe client's
// _carries_model_output check.
func frameCarriesOutput(frame []byte) bool {
	data := bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(frame), []byte("data:")))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return false
	}
	choices, _ := doc["choices"].([]any)
	for _, item := range choices {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			delta, _ = choice["message"].(map[string]any)
		}
		if delta == nil {
			continue
		}
		if text, _ := delta["content"].(string); text != "" {
			return true
		}
		if reasoning, _ := delta["reasoning_content"].(string); reasoning != "" {
			return true
		}
		if calls, _ := delta["tool_calls"].([]any); len(calls) > 0 {
			return true
		}
	}
	return false
}

// directStreamError reads a small error body from a rejected direct stream.
func directStreamError(resp *http.Response) error {
	message, _ := io.ReadAll(io.LimitReader(resp.Body, 800))
	_ = resp.Body.Close()
	text := truncateText(string(message), 800)
	if text == "" {
		text = fmt.Sprintf("upstream status %d", resp.StatusCode)
	}
	return fmt.Errorf("%s", text)
}
