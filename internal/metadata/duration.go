package metadata

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"strings"
)

// probeDuration returns a best-effort duration in milliseconds. A zero value is
// intentionally stored for malformed or unsupported files so they do not force
// a full metadata rescan on every server start.
func probeDuration(path, container string) int64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()

	switch strings.ToLower(container) {
	case "mp3":
		return mp3Duration(file)
	case "m4a", "mp4":
		return mp4Duration(file)
	case "aac":
		return adtsDuration(file)
	case "ogg", "oga", "opus":
		return oggDuration(file)
	default:
		return 0
	}
}

func flacStreamInfoDuration(block []byte) int64 {
	if len(block) < 18 {
		return 0
	}
	packed := binary.BigEndian.Uint64(block[10:18])
	sampleRate := packed >> 44
	totalSamples := packed & ((uint64(1) << 36) - 1)
	if sampleRate == 0 || totalSamples == 0 {
		return 0
	}
	return int64(totalSamples * 1000 / sampleRate)
}

func mp3Duration(file *os.File) int64 {
	reader := bufio.NewReaderSize(file, 64*1024)
	if header, _ := reader.Peek(10); len(header) == 10 && string(header[:3]) == "ID3" {
		size := int64(header[6]&0x7f)<<21 | int64(header[7]&0x7f)<<14 | int64(header[8]&0x7f)<<7 | int64(header[9]&0x7f)
		if _, err := file.Seek(size+10, io.SeekStart); err != nil {
			return 0
		}
		reader.Reset(file)
	}

	var totalSamples int64
	var sampleRate int64
	for {
		header, err := reader.Peek(4)
		if err != nil {
			break
		}
		frameSize, samples, rate, ok := mp3FrameInfo(binary.BigEndian.Uint32(header))
		if !ok {
			_, _ = reader.ReadByte()
			continue
		}
		if _, err = reader.Discard(frameSize); err != nil {
			break
		}
		totalSamples += int64(samples)
		sampleRate = int64(rate)
	}
	if totalSamples == 0 || sampleRate == 0 {
		return 0
	}
	return totalSamples * 1000 / sampleRate
}

func mp3FrameInfo(header uint32) (frameSize, samples, sampleRate int, ok bool) {
	if header&0xffe00000 != 0xffe00000 {
		return 0, 0, 0, false
	}
	version := (header >> 19) & 0x3
	layer := (header >> 17) & 0x3
	bitrateIndex := int((header >> 12) & 0xf)
	rateIndex := int((header >> 10) & 0x3)
	padding := int((header >> 9) & 0x1)
	if version == 1 || layer == 0 || bitrateIndex == 0 || bitrateIndex == 15 || rateIndex == 3 {
		return 0, 0, 0, false
	}

	rates := []int{44100, 48000, 32000}
	sampleRate = rates[rateIndex]
	if version == 2 {
		sampleRate /= 2
	} else if version == 0 {
		sampleRate /= 4
	}

	versionOne := version == 3
	var bitrates []int
	switch layer {
	case 3:
		bitrates = []int{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448}
		samples = 384
		frameSize = (12*bitrates[bitrateIndex]*1000/sampleRate + padding) * 4
	case 2:
		bitrates = []int{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384}
		samples = 1152
		frameSize = 144*bitrates[bitrateIndex]*1000/sampleRate + padding
	case 1:
		if versionOne {
			bitrates = []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
			samples = 1152
			frameSize = 144*bitrates[bitrateIndex]*1000/sampleRate + padding
		} else {
			bitrates = []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
			samples = 576
			frameSize = 72*bitrates[bitrateIndex]*1000/sampleRate + padding
		}
	}
	return frameSize, samples, sampleRate, frameSize > 4
}

func adtsDuration(file *os.File) int64 {
	reader := bufio.NewReaderSize(file, 64*1024)
	rates := []int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
	var totalSamples int64
	var sampleRate int64
	for {
		header, err := reader.Peek(7)
		if err != nil {
			break
		}
		if header[0] != 0xff || header[1]&0xf6 != 0xf0 {
			_, _ = reader.ReadByte()
			continue
		}
		rateIndex := int((header[2] >> 2) & 0xf)
		frameLength := int(header[3]&0x3)<<11 | int(header[4])<<3 | int(header[5]>>5)
		if rateIndex >= len(rates) || frameLength < 7 {
			_, _ = reader.ReadByte()
			continue
		}
		if _, err = reader.Discard(frameLength); err != nil {
			break
		}
		totalSamples += int64((int(header[6]&0x3) + 1) * 1024)
		sampleRate = int64(rates[rateIndex])
	}
	if totalSamples == 0 || sampleRate == 0 {
		return 0
	}
	return totalSamples * 1000 / sampleRate
}

func oggDuration(file *os.File) int64 {
	reader := bufio.NewReaderSize(file, 64*1024)
	var sampleRate uint64
	var lastGranule uint64
	for {
		header := make([]byte, 27)
		if _, err := io.ReadFull(reader, header); err != nil {
			break
		}
		if string(header[:4]) != "OggS" {
			return 0
		}
		segments := int(header[26])
		laces := make([]byte, segments)
		if _, err := io.ReadFull(reader, laces); err != nil {
			return 0
		}
		payloadSize := 0
		for _, value := range laces {
			payloadSize += int(value)
		}
		payload := make([]byte, payloadSize)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return 0
		}
		if sampleRate == 0 {
			switch {
			case len(payload) >= 8 && string(payload[:8]) == "OpusHead":
				sampleRate = 48000
			case len(payload) >= 16 && payload[0] == 1 && string(payload[1:7]) == "vorbis":
				sampleRate = uint64(binary.LittleEndian.Uint32(payload[12:16]))
			}
		}
		granule := binary.LittleEndian.Uint64(header[6:14])
		if granule != ^uint64(0) {
			lastGranule = granule
		}
	}
	if sampleRate == 0 || lastGranule == 0 {
		return 0
	}
	return int64(lastGranule * 1000 / sampleRate)
}

func mp4Duration(file *os.File) int64 {
	info, err := file.Stat()
	if err != nil {
		return 0
	}
	return findMP4Duration(file, 0, info.Size())
}

func findMP4Duration(file *os.File, start, end int64) int64 {
	for offset := start; offset+8 <= end; {
		header := make([]byte, 16)
		if _, err := file.ReadAt(header[:8], offset); err != nil {
			return 0
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:8])
		headerSize := int64(8)
		if size == 1 {
			if _, err := file.ReadAt(header[8:16], offset+8); err != nil {
				return 0
			}
			size = int64(binary.BigEndian.Uint64(header[8:16]))
			headerSize = 16
		} else if size == 0 {
			size = end - offset
		}
		if size < headerSize || offset+size > end {
			return 0
		}
		payloadStart := offset + headerSize
		payloadEnd := offset + size
		switch kind {
		case "moov", "trak", "mdia":
			if duration := findMP4Duration(file, payloadStart, payloadEnd); duration > 0 {
				return duration
			}
		case "mdhd":
			payload := make([]byte, 32)
			length := payloadEnd - payloadStart
			if length < 20 {
				return 0
			}
			if int64(len(payload)) > length {
				payload = payload[:length]
			}
			if _, err := file.ReadAt(payload, payloadStart); err != nil {
				return 0
			}
			if payload[0] == 1 && len(payload) >= 32 {
				timescale := uint64(binary.BigEndian.Uint32(payload[20:24]))
				duration := binary.BigEndian.Uint64(payload[24:32])
				if timescale > 0 {
					return int64(duration * 1000 / timescale)
				}
			} else if len(payload) >= 20 {
				timescale := uint64(binary.BigEndian.Uint32(payload[12:16]))
				duration := uint64(binary.BigEndian.Uint32(payload[16:20]))
				if timescale > 0 {
					return int64(duration * 1000 / timescale)
				}
			}
		}
		offset += size
	}
	return 0
}
