package mesh

import (
	"bytes"
	"sync"
)

// LogBuffer 是一个并发安全的行级环形缓冲区，实现了 io.Writer 端口
type LogBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
	buf   []byte // 用于暂存未遇到换行符的残缺字节
}

// NewLogBuffer 创造指定最大行数的日志截获器
func NewLogBuffer(maxLines int) *LogBuffer {
	return &LogBuffer{
		max:   maxLines,
		lines: make([]string, 0, maxLines),
	}
}

// Write 拦截操作系统的标准输出流，按换行符进行切分缓存
func (b *LogBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf = append(b.buf, p...)

	for {
		idx := bytes.IndexByte(b.buf, '\n')
		if idx == -1 {
			break
		}

		line := string(b.buf[:idx])
		b.buf = b.buf[idx+1:] // 截取已读取的部分

		if len(b.lines) >= b.max {
			// 容量已满，剔除最旧的一行（切片左移）
			b.lines = append(b.lines[1:], line)
		} else {
			b.lines = append(b.lines, line)
		}
	}
	return len(p), nil
}

// Lines 返回当前缓存的所有完整日志行快照
func (b *LogBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	snapshot := make([]string, len(b.lines))
	copy(snapshot, b.lines)
	return snapshot
}
