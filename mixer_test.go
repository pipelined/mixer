package mixer_test

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pipelined/mixer"
	"github.com/pipelined/signal"
)

// type track struct {
// 	messages  int
// 	value     float64
// 	interrupt bool
// }

func TestMixer(t *testing.T) {
	const numTracks = 2
	tests := []struct {
		description string
		messages    [numTracks]int
		values      [numTracks]float64
		expected    [][]float64
	}{
		{
			description: "1st run",
			messages: [numTracks]int{
				4,
				3,
			},
			values: [numTracks]float64{
				0.7,
				0.5,
			},
			expected: [][]float64{{0.6, 0.6, 0.6, 0.6, 0.6, 0.6, 0.7, 0.7}},
		},
		{
			description: "2nd run",
			messages: [numTracks]int{
				5,
				4,
			},
			values: [numTracks]float64{
				0.5,
				0.7,
			},
			expected: [][]float64{{0.6, 0.6, 0.6, 0.6, 0.6, 0.6, 0.6, 0.6, 0.5, 0.5}},
		},
	}

	var (
		err         error
		sampleRate  = signal.SampleRate(44100)
		numChannels = 1
		bufferSize  = 2
		pumpID      = string("pumpID")
	)

	mixer := mixer.New(numChannels)

	// init sink funcs
	sinks := make([]func(signal.Float64) error, numTracks)
	for i := 0; i < numTracks; i++ {
		sinks[i], err = mixer.Sink(string(i), sampleRate, numChannels)
		assert.Nil(t, err)
	}
	// init pump func
	pump, _, _, err := mixer.Pump(pumpID)
	assert.Nil(t, err)

	for _, test := range tests {
		// reset all
		for i := 0; i < numTracks; i++ {
			err = mixer.Reset(string(i))
			assert.Nil(t, err)
		}
		err = mixer.Reset(pumpID)
		assert.Nil(t, err)

		// mixing cycle
		result := signal.Float64(make([][]float64, numChannels))

		var sent = 0
		for {
			for i := 0; i < numTracks; i++ {
				// check if track is done
				if test.messages[i] == sent {
					err = mixer.Flush(string(i))
					assert.NoError(t, err)
				} else if test.messages[i] > sent {
					buf := buf(numChannels, bufferSize, test.values[i])
					err = sinks[i](buf)
					assert.NoError(t, err)
				}
			}
			buffer := signal.Float64Buffer(numChannels, bufferSize)
			err = pump(buffer)
			if err != nil {
				assert.Equal(t, io.EOF, err)
				break
			}
			if buffer != nil {
				result = result.Append(buffer)
				assert.NoError(t, err)
			}
			sent++
		}
		err = mixer.Flush(pumpID)
		assert.NoError(t, err)

		assert.Equal(t, len(test.expected), result.NumChannels(), "Incorrect result num channels")
		for i := range test.expected {
			assert.Equal(t, len(test.expected[i]), len(result[i]), "Incorrect result channel length")
			for j, val := range test.expected[i] {
				assert.Equal(t, val, result[i][j])
			}
		}
	}
}

func buf(numChannels, size int, value float64) [][]float64 {
	result := make([][]float64, numChannels)
	for i := range result {
		result[i] = make([]float64, size)
		for j := range result[i] {
			result[i][j] = value
		}
	}
	return result
}
