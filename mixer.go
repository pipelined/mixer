package mixer

import (
	"fmt"
	"io"
	"sync"

	"github.com/pipelined/signal"
)

// Mixer summs up multiple channels of messages into a single channel.
type Mixer struct {
	sampleRate  signal.SampleRate
	numChannels int

	// channel to send frames ready for mix
	output chan *frame

	// mutex is needed to synchronize access to the mixer
	m sync.Mutex
	// inputs
	inputs map[string]*input
	// number of active inputs, used to fill the frames
	activeInputs int
	// first frame, used to initialize inputs
	firstFrame *frame
	// id of the pipe which is output of mixer
	outputID string
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
		output:      make(chan *frame),
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
	m.activeInputs++

	return func(b signal.Float64) error {
		in.frame.Lock()
		done := in.frame.add(b)
		// move input to the next frame.
		if in.frame.next == nil {
			in.frame.next = newFrame(m.activeInputs, m.numChannels)
		}
		fmt.Printf("")
		in.frame.Unlock()

		// send if done.
		if done {
			m.output <- in.frame
		}

		in.frame = in.frame.next
		return nil
	}, nil
}

// Pump returns a pump function which allows to read the out channel.
func (m *Mixer) Pump(outputID string) (func(signal.Float64) error, signal.SampleRate, int, error) {
	numChannels := m.numChannels
	m.outputID = outputID
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

// Flush mixer data for defined source.
func (m *Mixer) Flush(sourceID string) error {
	m.m.Lock()
	defer m.m.Unlock()
	if m.isOutput(sourceID) {
		m.output = make(chan *frame)
		m.firstFrame = newFrame(len(m.inputs), m.numChannels)
		for _, in := range m.inputs {
			in.frame = m.firstFrame
		}
		return nil
	}

	// remove input from actives.
	m.activeInputs--
	fmt.Printf("Flush: %s Active inputs: %d\n", sourceID, m.activeInputs)
	// reset expectations for remaining frames.
	in := m.inputs[sourceID]
	for in.frame != nil {
		in.frame.Lock()
		in.frame.expected = int(m.activeInputs)
		in.frame.Unlock()

		// send if complete.
		if in.frame.isComplete() {
			m.output <- in.frame
		}
		fmt.Printf("Flushing frame: %+v\n", in.frame)
		// move to the next.
		in.frame = in.frame.next
	}
	if m.activeInputs == 0 {
		close(m.output)
	}
	return nil
}

func (m *Mixer) isOutput(sourceID string) bool {
	return sourceID == m.outputID
}

// isReady checks if frame is completed.
func (f *frame) isComplete() bool {
	return f.expected > 0 && f.expected == f.summed
}

// newFrame generates new frame based on number of inputs.
func newFrame(numInputs, numChannels int) *frame {
	return &frame{
		expected: numInputs,
		buffer:   make([][]float64, numChannels),
	}
}
