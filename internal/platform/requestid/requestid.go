package requestid

import (
	"bytes"
	"context"
	"runtime"
	"strconv"
	"sync"
)

type ctxKey struct{}

type idStack struct {
	mu  sync.Mutex
	ids []string
}

var stacks sync.Map // goid -> *idStack

// With 将 trace id 写入 context，供跨 goroutine 传播。
func With(ctx context.Context, id string) context.Context {
	if ctx == nil || id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

// From 从 context 读取 trace id；不存在时返回空字符串。
func From(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Bind 将 trace id 绑定到当前 goroutine（可嵌套），供 logger.Info 等自动带上。
func Bind(id string) {
	if id == "" {
		return
	}
	s := stackFor(goid())
	s.mu.Lock()
	s.ids = append(s.ids, id)
	s.mu.Unlock()
}

// Unbind 解除当前 goroutine 最近一次 Bind。
func Unbind() {
	gid := goid()
	v, ok := stacks.Load(gid)
	if !ok {
		return
	}
	s := v.(*idStack)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ids) == 0 {
		stacks.Delete(gid)
		return
	}
	s.ids = s.ids[:len(s.ids)-1]
	if len(s.ids) == 0 {
		stacks.Delete(gid)
	}
}

// Current 返回当前 goroutine 绑定的 trace id。
func Current() string {
	v, ok := stacks.Load(goid())
	if !ok {
		return ""
	}
	s := v.(*idStack)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ids) == 0 {
		return ""
	}
	return s.ids[len(s.ids)-1]
}

// Attach 若 ctx 含 trace id，则 Bind 到当前 goroutine，返回的函数用于 Unbind。
func Attach(ctx context.Context) func() {
	id := From(ctx)
	if id == "" {
		return func() {}
	}
	Bind(id)
	return Unbind
}

func stackFor(gid int64) *idStack {
	if v, ok := stacks.Load(gid); ok {
		return v.(*idStack)
	}
	s := &idStack{}
	actual, _ := stacks.LoadOrStore(gid, s)
	return actual.(*idStack)
}

func goid() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// "goroutine 123 [running]:..."
	b := bytes.TrimPrefix(buf[:n], []byte("goroutine "))
	i := bytes.IndexByte(b, ' ')
	if i <= 0 {
		return 0
	}
	id, err := strconv.ParseInt(string(b[:i]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
