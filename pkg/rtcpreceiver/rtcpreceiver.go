// Package rtcpreceiver implements a utility to generate RTCP receiver reports.
package rtcpreceiver

import (
	"math/rand"
	"sync"
	"time"

	"github.com/pion/rtcp"

	"github.com/aler9/gortsplib/pkg/base"
)

// RtcpReceiver is a utility to generate RTCP receiver reports.
type RtcpReceiver struct {
	receiverSSRC uint32
	clockRate    float64
	mutex        sync.Mutex

	// data from rtp packets
	firstRtpReceived     bool
	sequenceNumberCycles uint16
	lastSequenceNumber   uint16
	lastRtpTimeRtp       uint32
	lastRtpTimeTime      time.Time
	totalLost            uint32
	totalLostSinceReport uint32
	totalSinceReport     uint32
	jitter               float64

	// data from rtcp packets
	senderSSRC           uint32
	lastSenderReport     uint32
	lastSenderReportTime time.Time
}

// New allocates a RtcpReceiver.
func New(receiverSSRC *uint32, clockRate int) *RtcpReceiver {
	return &RtcpReceiver{
		receiverSSRC: func() uint32 {
			if receiverSSRC == nil {
				return rand.Uint32()
			}
			return *receiverSSRC
		}(),
		clockRate: float64(clockRate),
	}
}

// ProcessFrame extracts the needed data from RTP or RTCP frames.
func (rr *RtcpReceiver) ProcessFrame(ts time.Time, streamType base.StreamType, buf []byte) {
	rr.mutex.Lock()
	defer rr.mutex.Unlock()

	if streamType == base.StreamTypeRtp {
		// do not parse the entire packet, extract only the fields we need
		if len(buf) >= 8 {
			sequenceNumber := uint16(buf[2])<<8 | uint16(buf[3])
			rtpTime := uint32(buf[4])<<24 | uint32(buf[5])<<16 | uint32(buf[6])<<8 | uint32(buf[7])

			// first frame
			if !rr.firstRtpReceived {
				rr.firstRtpReceived = true
				rr.totalSinceReport = 1
				rr.lastSequenceNumber = sequenceNumber
				rr.lastRtpTimeRtp = rtpTime
				rr.lastRtpTimeTime = ts

				// subsequent frames
			} else {
				diff := int32(sequenceNumber) - int32(rr.lastSequenceNumber)

				// following frame or following frame after an overflow
				if diff > 0 || diff < -0x0FFF {
					// overflow
					if diff < -0x0FFF {
						rr.sequenceNumberCycles += 1
					}

					// detect lost frames
					if sequenceNumber != (rr.lastSequenceNumber + 1) {
						rr.totalLost += uint32(uint16(diff) - 1)
						rr.totalLostSinceReport += uint32(uint16(diff) - 1)

						// allow up to 24 bits
						if rr.totalLost > 0xFFFFFF {
							rr.totalLost = 0xFFFFFF
						}
						if rr.totalLostSinceReport > 0xFFFFFF {
							rr.totalLostSinceReport = 0xFFFFFF
						}
					}

					// compute jitter
					// https://tools.ietf.org/html/rfc3550#page-39
					D := ts.Sub(rr.lastRtpTimeTime).Seconds()*rr.clockRate -
						(float64(rtpTime) - float64(rr.lastRtpTimeRtp))
					if D < 0 {
						D = -D
					}
					rr.jitter += (D - rr.jitter) / 16

					rr.totalSinceReport += uint32(uint16(diff))
					rr.lastSequenceNumber = sequenceNumber
					rr.lastRtpTimeRtp = rtpTime
					rr.lastRtpTimeTime = ts
				}
				// ignore invalid frames (diff = 0) or reordered frames (diff < 0)
			}
		}

	} else {
		// we can afford to unmarshal all RTCP frames
		// since they are sent with a frequency much lower than the one of RTP frames
		frames, err := rtcp.Unmarshal(buf)
		if err == nil {
			for _, frame := range frames {
				if sr, ok := (frame).(*rtcp.SenderReport); ok {
					rr.senderSSRC = sr.SSRC
					rr.lastSenderReport = uint32(sr.NTPTime >> 16)
					rr.lastSenderReportTime = ts
				}
			}
		}
	}
}

// Report generates a RTCP receiver report.
func (rr *RtcpReceiver) Report(ts time.Time) []byte {
	rr.mutex.Lock()
	defer rr.mutex.Unlock()

	report := &rtcp.ReceiverReport{
		SSRC: rr.receiverSSRC,
		Reports: []rtcp.ReceptionReport{
			{
				SSRC:               rr.senderSSRC,
				LastSequenceNumber: uint32(rr.sequenceNumberCycles)<<16 | uint32(rr.lastSequenceNumber),
				LastSenderReport:   rr.lastSenderReport,
				// equivalent to taking the integer part after multiplying the
				// loss fraction by 256
				FractionLost: uint8(float64(rr.totalLostSinceReport*256) / float64(rr.totalSinceReport)),
				TotalLost:    rr.totalLost,
				// delay, expressed in units of 1/65536 seconds, between
				// receiving the last SR packet from source SSRC_n and sending this
				// reception report block
				Delay:  uint32(ts.Sub(rr.lastSenderReportTime).Seconds() * 65536),
				Jitter: uint32(rr.jitter),
			},
		},
	}

	rr.totalLostSinceReport = 0
	rr.totalSinceReport = 0

	byts, err := report.Marshal()
	if err != nil {
		panic(err)
	}

	return byts
}