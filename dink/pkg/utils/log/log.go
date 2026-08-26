package log

import (
	"context"
	"log/slog"
)

var G = GetLogger

type loggerKey struct{}

type Logger struct {
	log *slog.Logger
	ctx context.Context
}

func GetLogger(ctx context.Context) *Logger {
	if ctx == nil {
		return &Logger{log: slog.Default(), ctx: context.Background()}
	}
	if l, ok := ctx.Value(loggerKey{}).(*Logger); ok {
		return &Logger{log: l.log, ctx: ctx}
	}
	return &Logger{log: slog.Default(), ctx: ctx}
}

func WithLogger(ctx context.Context, logger *Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, &Logger{log: logger.log, ctx: ctx})
}

func (l *Logger) Debug(msg string, args ...any) {
	l.log.DebugContext(l.ctx, msg, args...)
}

func (l *Logger) Info(msg string, args ...any) {
	l.log.InfoContext(l.ctx, msg, args...)
}

func (l *Logger) Warn(msg string, args ...any) {
	l.log.WarnContext(l.ctx, msg, args...)
}

func (l *Logger) Error(msg string, args ...any) {
	l.log.ErrorContext(l.ctx, msg, args...)
}

func (l *Logger) Log(level slog.Level, msg string, args ...any) {
	l.log.Log(l.ctx, level, msg, args...)
}

func (l *Logger) LogAttrs(level slog.Level, msg string, attrs ...slog.Attr) {
	l.log.LogAttrs(l.ctx, level, msg, attrs...)
}

func (l *Logger) WithContext(ctx context.Context) *Logger {
	return &Logger{log: l.log, ctx: ctx}
}

func (l *Logger) WithError(err error) *Logger {
	return &Logger{log: l.log.With("error", err), ctx: l.ctx}
}

func (l *Logger) With(args ...any) *Logger {
	return &Logger{log: l.log.With(args...), ctx: l.ctx}
}

func (l *Logger) WithGroup(name string) *Logger {
	return &Logger{log: l.log.WithGroup(name), ctx: l.ctx}
}
