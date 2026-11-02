package dispatcher

import (
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/features/stats"
)

type SizeStatWriter struct {
	Counter stats.Counter
	Writer  buf.Writer
}

func (w *SizeStatWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	w.Counter.Add(int64(mb.Len()))
	return w.Writer.WriteMultiBuffer(mb)
}

func (w *SizeStatWriter) Close() error {
	return common.Close(w.Writer)
}

func (w *SizeStatWriter) Interrupt() {
	common.Interrupt(w.Writer)
}

// joinCounters returns a counter that adds to both a and b. Either may be nil.
func joinCounters(a, b stats.Counter) stats.Counter {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return &pairCounter{a: a, b: b}
}

// pairCounter counts the same bytes twice: on the account counter (a) and on
// the source IP counter (b). Value and Set act on a.
type pairCounter struct {
	a, b stats.Counter
}

func (c *pairCounter) Value() int64 { return c.a.Value() }

func (c *pairCounter) Set(v int64) int64 { return c.a.Set(v) }

func (c *pairCounter) Add(delta int64) int64 {
	c.b.Add(delta)
	return c.a.Add(delta)
}
