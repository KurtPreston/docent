package ai

import "io"

// callbackWriter is an io.Writer that forwards every Write to an optional
// string callback. Used to tee CLI stdout/stderr into OnContent/OnThinking
// without changing the buffered copy kept for debug logs.
type callbackWriter struct {
	fn func(string)
}

func (w callbackWriter) Write(p []byte) (int, error) {
	if w.fn != nil && len(p) > 0 {
		w.fn(string(p))
	}
	return len(p), nil
}

// teeCallback returns a writer that copies to dst and, when on is non-nil,
// also invokes the callback with each chunk.
func teeCallback(dst io.Writer, on func(string)) io.Writer {
	if on == nil {
		return dst
	}
	return io.MultiWriter(dst, callbackWriter{fn: on})
}

// teeThinking wires stderr to a buffer, optional StreamOut, and OnThinking.
func teeThinking(dst io.Writer, streamOut io.Writer, onThinking func(string)) io.Writer {
	writers := []io.Writer{dst}
	if streamOut != nil {
		writers = append(writers, streamOut)
	}
	if onThinking != nil {
		writers = append(writers, callbackWriter{fn: onThinking})
	}
	if len(writers) == 1 {
		return writers[0]
	}
	return io.MultiWriter(writers...)
}
