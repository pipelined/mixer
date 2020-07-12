package mixer_test

import (
	"context"
	"reflect"
	"testing"

	"pipelined.dev/mixer"
	"pipelined.dev/pipe"
	"pipelined.dev/pipe/mock"
	"pipelined.dev/signal"
)

func TestMixer(t *testing.T) {
	type generator struct {
		messages int
		value    float64
	}
	tests := []struct {
		description string
		generators  []generator
		expected    []float64
	}{
		{
			description: "1st run",
			generators: []generator{
				{
					messages: 8,
					value:    0.7,
				},
				{
					messages: 6,
					value:    0.5,
				},
			},
			expected: []float64{0.6, 0.6, 0.6, 0.6, 0.6, 0.6, 0.7, 0.7},
		},
		{
			description: "2nd run",
			generators: []generator{
				{
					messages: 5,
					value:    0.5,
				},
				{
					messages: 4,
					value:    0.7,
				},
			},
			expected: []float64{0.6, 0.6, 0.6, 0.6, 0.5, 0},
		},
	}

	const (
		// sampleRate  = signal.SampleRate(44100)
		numChannels = 1
		bufferSize  = 2
	)
	for _, test := range tests {
		mixer := mixer.New(numChannels)

		var lines []pipe.Line
		for _, gen := range test.generators {
			sourceAllocator := mock.Source{
				Channels: numChannels,
				Limit:    gen.messages,
				Value:    gen.value,
			}
			line, _ := pipe.Routing{
				Source: sourceAllocator.Source(),
				Sink:   mixer.Sink(),
			}.Line(bufferSize)
			lines = append(lines, line)
		}
		sink := mock.Sink{}
		line, _ := pipe.Routing{
			Source: mixer.Source(),
			Sink:   sink.Sink(),
		}.Line(bufferSize)
		lines = append(lines, line)

		err := pipe.New(context.Background(), pipe.WithLines(lines...)).Wait()
		assertEqual(t, "error", err, nil)

		result := make([]float64, sink.Values.Len())
		signal.ReadFloat64(sink.Values, result)
		assertEqual(t, "result", result, test.expected)
	}
}

func assertEqual(t *testing.T, name string, result, expected interface{}) {
	t.Helper()
	if !reflect.DeepEqual(expected, result) {
		t.Fatalf("%v\nresult: \t%T\t%+v \nexpected: \t%T\t%+v", name, result, result, expected, expected)
	}
}
