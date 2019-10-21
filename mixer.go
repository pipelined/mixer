package mixer

import (
	"io"
	"sync"
	"sync/atomic"

	"github.com/pipelined/signal"
)

// Mixer summs up multiple channels of messages into a single channel.
type Mixer struct {
	sampleRate  signal.SampleRate
	numChannels int

	active   int32             // number of active inputs
	inputs   map[string]*input // inputs
	outputID atomic.Value      // id of the pipe which is output of mixer

	firstFrame *frame // first frame, used only to initialize inputs

	m      sync.Mutex  // mutex is needed to synchronize flushing
	output chan *frame // channel to send frames ready for mix
}

type message struct {
	inputID string
	buffer  signal.Float64
}

type input struct {
	id          string
	numChannels int
	frame       *frame
}

// frame represents a slice of samples to mix.
type frame struct {
	sync.Mutex
	buffer   signal.Float64
	summed   int
	expected int
	next     *frame
}

// sum returns mixed samplein.
func (f *frame) copySum(b signal.Float64) {
	// shrink result buffer if needed.
	if b.Size() > f.buffer.Size() {
		for i := range f.buffer {
			b[i] = b[i][:f.buffer.Size()]
		}
	}
	// copy summed data.
	for i := 0; i < b.NumChannels(); i++ {
		for j := 0; j < b.Size(); j++ {
			b[i][j] = f.buffer[i][j] / float64(f.summed)
		}
	}
}

func (f *frame) add(b signal.Float64) bool {
	// expand frame buffer if needed.
	if diff := b.Size() - f.buffer.Size(); diff > 0 {
		for i := range f.buffer {
			f.buffer[i] = append(f.buffer[i], make([]float64, diff)...)
		}
	}

	// copy summed data.
	for i := 0; i < b.NumChannels(); i++ {
		for j := 0; j < b.Size(); j++ {
			f.buffer[i][j] += b[i][j]
		}
	}
	f.summed++
	return f.isComplete()
}

const (
	maxInputs = 1024
)

// New returns new mixer.
func New(numChannels int) *Mixer {
	m := Mixer{
		firstFrame:  newFrame(0, numChannels),
		inputs:      make(map[string]*input),
		numChannels: numChannels,
	}
	return &m
}

// Sink registers new input. All inputs should have same number of channels.
// If different number of channels is provided, error will be returned.
func (m *Mixer) Sink(inputID string, sampleRate signal.SampleRate, numChannels int) (func(signal.Float64) error, error) {
	m.sampleRate = sampleRate
	m.firstFrame.expected++
	in := input{
		id:          inputID,
		frame:       m.firstFrame,
		numChannels: numChannels,
	}
	// add new input.
	m.inputs[inputID] = &in

	return func(b signal.Float64) error {
		in.frame.Lock()
		done := in.frame.add(b)
		// move input to the next frame.
		if in.frame.next == nil {
			in.frame.next = newFrame(atomic.LoadInt32(&m.active), m.numChannels)
		}
		in.frame.Unlock()

		// send if done.
		if done {
			m.output <- in.frame
		}

		in.frame = in.frame.next
		return nil
	}, nil
}

// Reset resets the mixer for another run.
func (m *Mixer) Reset(sourceID string) error {
	if m.isOutput(sourceID) {
		m.output = make(chan *frame, 1)
		m.firstFrame = newFrame(int32(len(m.inputs)), m.numChannels)
	} else {
		atomic.AddInt32(&m.active, 1)
	}
	return nil
}

// Flush mixer data for defined source.
func (m *Mixer) Flush(sourceID string) error {
	if m.isOutput(sourceID) {
		return nil
	}

	m.m.Lock()
	defer m.m.Unlock()
	// remove input from actives.
	active := atomic.AddInt32(&m.active, -1)

	// reset expectations for remaining frames.
	in := m.inputs[sourceID]
	for in.frame != nil {
		in.frame.Lock()
		in.frame.expected = int(active)
		in.frame.Unlock()

		// send if complete.
		if in.frame.isComplete() {
			m.output <- in.frame
		}

		// move to the next.
		in.frame = in.frame.next
	}
	if active == 0 {
		close(m.output)
	}
	return nil
}

func (m *Mixer) isOutput(sourceID string) bool {
	return sourceID == m.outputID.Load().(string)
}

// Pump returns a pump function which allows to read the out channel.
func (m *Mixer) Pump(outputID string) (func(signal.Float64) error, signal.SampleRate, int, error) {
	numChannels := m.numChannels
	m.outputID.Store(outputID)
	return func(b signal.Float64) error {
		// receive new buffer
		f, ok := <-m.output
		if !ok {
			return io.EOF
		}
		f.copySum(b)
		return nil
	}, m.sampleRate, numChannels, nil
}

// isReady checks if frame is completed.
func (f *frame) isComplete() bool {
	return f.expected > 0 && f.expected == f.summed
}

// newFrame generates new frame based on number of inputs.
func newFrame(numInputs int32, numChannels int) *frame {
	return &frame{
		expected: int(numInputs),
		buffer:   make([][]float64, numChannels),
	}
}
