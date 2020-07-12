package mixer

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"unsafe"

	"pipelined.dev/pipe"
	"pipelined.dev/signal"
)

// Mixer summs up multiple channels of messages into a single channel.
type Mixer struct {
	sampleRate  signal.SampleRate
	numChannels int

	// frames ready for mix
	output chan *frame
	// signal that all sinks are done
	done chan struct{}

	// mutex is needed to synchronize access to the mixer
	m sync.Mutex
	// totalInputs int
	// number of active inputs, used to shutdown mixer
	activeInputs int32
	// last frame, used to initialize inputs
	tail atomic.Value
}

// frame represents a slice of samples to mix.
type frame struct {
	buffer   unsafe.Pointer
	added    int32
	expected int32
	next     unsafe.Pointer
}

// sum returns mixed samplein.
func (f *frame) sum(out signal.Floating) {
	summs := f.loadBuffer()
	offset := out.Len()
	for i := 0; i < out.Len(); i++ {
		var sum float64
		for j := 0; j < int(f.added); j++ {
			sum += summs.Sample(i + j*offset)
		}
		out.SetSample(i, sum/float64(f.added))
	}
}

// offset is the offset in the sums buffer
func add(offset int, summs, floats signal.Floating) {
	// copy summed data.
	for i := 0; i < floats.Len(); i++ {
		summs.SetSample(offset+i, floats.Sample(i))
	}
}

// New returns new mixer.
func New(channels int) *Mixer {
	m := Mixer{
		numChannels: channels,
		output:      make(chan *frame),
		done:        make(chan struct{}),
	}
	m.storeTail(&frame{})
	return &m
}

// Source provides mixer source allocator. Mixer source outputs mixed
// signal. Only single source per mixer is allowed.
func (m *Mixer) Source() pipe.SourceAllocatorFunc {
	return func(bufferSize int) (pipe.Source, pipe.SignalProperties, error) {
		return pipe.Source{
				SourceFunc: func(floats signal.Floating) (int, error) {
					select {
					case f := <-m.output: // receive new frame.
						f.sum(floats)
					case <-m.done: // recieve done signal.
						select {
						case f := <-m.output: // try to receive flushed frames.
							f.sum(floats)
						default:
							return 0, io.EOF
						}
					}
					return bufferSize, nil
				},
				FlushFunc: func(context.Context) error {
					m.output = make(chan *frame)
					m.done = make(chan struct{})
					return nil
				},
			}, pipe.SignalProperties{
				Channels:   m.numChannels,
				SampleRate: m.sampleRate,
			}, nil
	}
}

func (m *Mixer) storeTail(f *frame) {
	m.tail.Store(f)
}

func (m *Mixer) loadTail() *frame {
	return m.tail.Load().(*frame)
}

func (f *frame) storeBuffer(buf signal.Floating) bool {
	return atomic.CompareAndSwapPointer(&f.buffer, nil, unsafe.Pointer(&buf))
}

func (f *frame) loadBuffer() signal.Floating {
	if buf := atomic.LoadPointer(&f.buffer); buf != nil {
		return *(*signal.Floating)(buf)
	}
	return nil
}

func (f *frame) loadNext() *frame {
	if next := atomic.LoadPointer(&f.next); next != nil {
		return (*frame)(next)
	}
	return nil
}

// Sink provides mixer sink allocator. Mixer sink receives a signal for
// mixing. Multiple sinks per mixer is allowed.
func (m *Mixer) Sink() pipe.SinkAllocatorFunc {
	return func(bufferSize int, props pipe.SignalProperties) (pipe.Sink, error) {
		// TODO: handle different sample rates
		m.sampleRate = props.SampleRate
		m.activeInputs++
		current := m.loadTail()
		current.expected++
		return pipe.Sink{
			SinkFunc: func(floats signal.Floating) error {
				// sink new buffer
				var buf signal.Floating
				for {
					// buffer was allocated by another goroutine
					if buf = current.loadBuffer(); buf != nil {
						break
					}
					expected := atomic.LoadInt32(&current.expected)
					buf = signal.Allocator{
						Channels: props.Channels,
						Length:   bufferSize * props.Channels * int(expected),
						Capacity: bufferSize * props.Channels * int(expected),
					}.Float64()
					// is store succeeds, this goroutine won the allocation race
					if current.storeBuffer(buf) {
						break
					}
				}
				added := atomic.AddInt32(&current.added, 1)
				offset := int(added-1) * bufferSize * props.Channels
				add(offset, buf, floats)

				// get the next frame
				next := &frame{
					expected: current.expected,
				}
				if atomic.CompareAndSwapPointer(&current.next, nil, unsafe.Pointer(next)) {
					m.storeTail(next)
				} else {
					// other goroutine added next
					next = current.loadNext()
				}

				if current.done(added) {
					m.output <- current
				}
				current = next
				return nil
			},
			FlushFunc: func(context.Context) error {
				for current != nil {
					next := current.loadNext()
					atomic.AddInt32(&current.expected, -1)
					if current.done(atomic.LoadInt32(&current.added)) {
						m.output <- current
					}
					current = next
				}
				// remove input from actives.
				if atomic.AddInt32(&m.activeInputs, -1) == 0 {
					close(m.done)
				}
				return nil
			},
		}, nil
	}
}

// isReady checks if frame is completed.
func (f *frame) done(added int32) bool {
	expected := atomic.LoadInt32(&f.expected)
	return expected > 0 && expected == added
}

func min(n1, n2 int) int {
	if n1 < n2 {
		return n1
	}
	return n2
}
