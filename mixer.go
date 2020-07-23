package mixer

import (
	"context"
	"errors"
	"io"

	"pipelined.dev/pipe"
	"pipelined.dev/signal"
)

var (
	// ErrDifferentSampleRates is returned when signals with different
	// sample rates are sinked into mixer.
	ErrDifferentSampleRates = errors.New("sinking different sample rates")
	// ErrDifferentChannels is returned when signals with different number
	// of channels are sinked into mixer.
	ErrDifferentChannels = errors.New("sinking different channels")
	// ErrSinkFlushTimeout is returned when sink flush exceeds the context
	// timeout.
	ErrSinkFlushTimeout = errors.New("sink flush timeout")
)

// Mixer summs up multiple channels of messages into a single channel.
type Mixer struct {
	sampleRate  signal.SampleRate
	numChannels int

	pool        *signal.Pool
	inputSignal chan inputSignal

	head   *frame
	frames []*frame
}

// frame represents a slice of samples to mix.
type frame struct {
	next     *frame
	buffer   signal.Floating
	expected int
	added    int
	flushed  int
}

type inputSignal struct {
	input  int
	buffer signal.Floating
}

// sum returns mixed samplein.
func (f *frame) sum() bool {
	if f.added > 0 && f.added+f.flushed == f.expected {
		for i := 0; i < f.buffer.Len(); i++ {
			f.buffer.SetSample(i, f.buffer.Sample(i)/float64(f.added))
		}
		return true
	}
	return false
}

func (f *frame) add(in signal.Floating) {
	f.added++
	length := min(f.buffer.Len(), in.Len())
	for i := 0; i < length; i++ {
		f.buffer.SetSample(i, f.buffer.Sample(i)+in.Sample(i))
	}
	if f.buffer.Len() >= in.Len() {
		return
	}

	// todo: fix allocations here
	for i := length; i < in.Len(); i++ {
		f.buffer = f.buffer.AppendSample(in.Sample(i))
	}
	return
}

func mixer(pool *signal.Pool, frames []*frame, input <-chan inputSignal, output chan<- signal.Floating) {
	defer close(output)
	activeInputs := len(frames)
	for {
		if activeInputs == 0 {
			return
		}
		is := <-input
		f := frames[is.input]

		// flush the signal
		if is.buffer == nil {
			frames[is.input] = nil
			activeInputs--
			for current := f; current != nil; current = current.next {
				current.flushed++
				if current.sum() {
					output <- current.buffer
				}
			}
			continue
		}

		if f.buffer == nil {
			f.buffer = pool.GetFloat64()
		}
		f.add(is.buffer)
		pool.PutFloat64(is.buffer)
		if f.sum() {
			output <- f.buffer
		}
		if f.next == nil {
			// flushed sinks are not expected anymore
			f.next = &frame{
				expected: f.expected - f.flushed,
			}
		}
		frames[is.input] = f.next
	}
}

// New returns new mixer.
func New(channels int) *Mixer {
	return &Mixer{
		numChannels: channels,
		inputSignal: make(chan inputSignal, 1),
		head:        &frame{},
	}
}

// Source provides mixer source allocator. Mixer source outputs mixed
// signal. Only single source per mixer is allowed.
func (m *Mixer) Source() pipe.SourceAllocatorFunc {
	return func(bufferSize int) (pipe.Source, pipe.SignalProperties, error) {
		m.pool = signal.Allocator{
			Channels: m.numChannels,
			Capacity: bufferSize,
		}.Pool()
		outputSignal := make(chan signal.Floating, 1)
		go mixer(m.pool, m.frames, m.inputSignal, outputSignal)

		// this is needed to enable garbage collection
		m.frames = nil
		m.head = nil
		return pipe.Source{
				SourceFunc: func(out signal.Floating) (int, error) {
					if sum, ok := <-outputSignal; ok {
						defer m.pool.PutFloat64(sum)
						return signal.FloatingAsFloating(sum, out), nil
					}
					return 0, io.EOF
				},
				FlushFunc: func(context.Context) error {
					return nil
				},
			}, pipe.SignalProperties{
				Channels:   m.numChannels,
				SampleRate: m.sampleRate,
			}, nil
	}
}

// Sink provides mixer sink allocator. Mixer sink receives a signal for
// mixing. Multiple sinks per mixer is allowed.
func (m *Mixer) Sink() pipe.SinkAllocatorFunc {
	return func(bufferSize int, props pipe.SignalProperties) (pipe.Sink, error) {
		if m.sampleRate == 0 {
			m.sampleRate = props.SampleRate
		} else if m.sampleRate != props.SampleRate {
			return pipe.Sink{}, ErrDifferentSampleRates
		}
		if m.numChannels != props.Channels {
			return pipe.Sink{}, ErrDifferentChannels
		}
		input := len(m.frames)
		m.frames = append(m.frames, m.head)
		m.head.expected++
		return pipe.Sink{
			SinkFunc: func(floats signal.Floating) error {
				// sink new buffer
				inputBuffer := m.pool.GetFloat64().Slice(0, floats.Length())
				copied := signal.FloatingAsFloating(floats, inputBuffer)
				if copied != inputBuffer.Length() {
					inputBuffer = inputBuffer.Slice(0, copied)
				}
				m.inputSignal <- inputSignal{
					input:  input,
					buffer: inputBuffer,
				}
				return nil
			},
			FlushFunc: func(ctx context.Context) error {
				select {
				case m.inputSignal <- inputSignal{input: input}:
				case <-ctx.Done():
					return ErrSinkFlushTimeout
				}
				return nil
			},
		}, nil
	}
}

func min(n1, n2 int) int {
	if n1 < n2 {
		return n1
	}
	return n2
}
