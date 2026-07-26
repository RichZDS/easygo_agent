package logger

import (
	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
)

// prettyConsoleEncoder 输出：
//
//	2026-07-26T20:31:35[INFO] file.go:12 message {"k":"v"}
type prettyConsoleEncoder struct {
	zapcore.Encoder
	color bool
}

func newPrettyConsoleEncoder(color bool) zapcore.Encoder {
	cfg := zapcore.EncoderConfig{
		MessageKey:     "",
		LevelKey:       "",
		TimeKey:        "",
		NameKey:        "",
		CallerKey:      "",
		FunctionKey:    "",
		StacktraceKey:  "",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
	return &prettyConsoleEncoder{
		Encoder: zapcore.NewJSONEncoder(cfg),
		color:   color,
	}
}

func (e *prettyConsoleEncoder) Clone() zapcore.Encoder {
	return &prettyConsoleEncoder{
		Encoder: e.Encoder.Clone(),
		color:   e.color,
	}
}

func (e *prettyConsoleEncoder) EncodeEntry(entry zapcore.Entry, fields []zapcore.Field) (*buffer.Buffer, error) {
	buf := buffer.NewPool().Get()

	buf.AppendString(entry.Time.Format("2006-01-02T15:04:05"))
	buf.AppendByte('[')
	level := entry.Level.CapitalString()
	if e.color {
		buf.AppendString(colorizeLevel(entry.Level, level))
	} else {
		buf.AppendString(level)
	}
	buf.AppendByte(']')
	buf.AppendByte(' ')

	if entry.Caller.Defined {
		buf.AppendString(entry.Caller.TrimmedPath())
		buf.AppendByte(' ')
	}

	buf.AppendString(entry.Message)

	jsonBuf, err := e.Encoder.EncodeEntry(zapcore.Entry{
		Level: entry.Level,
		Time:  entry.Time,
	}, fields)
	if err != nil {
		buf.Free()
		return nil, err
	}
	raw := trimJSONLine(jsonBuf.Bytes())
	jsonBuf.Free()
	if len(raw) > 2 { // skip "{}"
		buf.AppendByte(' ')
		buf.AppendBytes(raw)
	}

	if entry.Stack != "" {
		buf.AppendByte('\n')
		buf.AppendString(entry.Stack)
	}
	buf.AppendString(zapcore.DefaultLineEnding)
	return buf, nil
}

func trimJSONLine(raw []byte) []byte {
	for len(raw) > 0 && (raw[len(raw)-1] == '\n' || raw[len(raw)-1] == '\r') {
		raw = raw[:len(raw)-1]
	}
	return raw
}

func colorizeLevel(level zapcore.Level, text string) string {
	const (
		reset  = "\033[0m"
		red    = "\033[31m"
		yellow = "\033[33m"
		blue   = "\033[34m"
		cyan   = "\033[36m"
		gray   = "\033[90m"
	)
	switch level {
	case zapcore.DebugLevel:
		return gray + text + reset
	case zapcore.InfoLevel:
		return blue + text + reset
	case zapcore.WarnLevel:
		return yellow + text + reset
	case zapcore.ErrorLevel, zapcore.DPanicLevel, zapcore.PanicLevel, zapcore.FatalLevel:
		return red + text + reset
	default:
		return cyan + text + reset
	}
}
