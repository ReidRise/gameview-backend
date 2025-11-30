package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/vladimirvivien/go4vl/device"
	"github.com/vladimirvivien/go4vl/v4l2"
)

var (
	dev  *device.Device
	once sync.Once
)

// initDevice opens /dev/video0 once, prefers H264 output. Returns nil and logs fatal if no H264.
func initDevice() *device.Device {
	once.Do(func() {

		// Open device with chosen pixfmt and FPS
		dev, err := device.Open(
			"/dev/video0",
			device.WithPixFormat(v4l2.PixFormat{PixelFormat: v4l2.PixelFmtH264, Width: 1280, Height: 720}),
			device.WithFPS(30),
		)
		if err != nil {
			log.Fatalf("failed to open device: %v", err)
		}

		if err := dev.Start(context.TODO()); err != nil {
			log.Fatalf("failed to start stream: %v", err)
		}

		log.Printf("Camera started: %dx%d (H264)", 1280, 720)
	})

	return dev
}

func main() {
	http.HandleFunc("/offer", handleOffer)
	// http.Handle("/", http.FileServer(http.Dir(".")))

	fmt.Println("Running on :9090")
	log.Fatal(http.ListenAndServe(":9090", nil))
}

func handleOffer(w http.ResponseWriter, r *http.Request) {
	// Ensure camera is opened (or fatal earlier)
	if initDevice() == nil {
		http.Error(w, "camera init failed", http.StatusInternalServerError)
		return
	}

	// Read body and parse the incoming offer JSON
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body error: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var offer webrtc.SessionDescription
	if err := json.Unmarshal(body, &offer); err != nil {
		http.Error(w, "invalid offer JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("Received offer: type=%s sdp_len=%d", offer.Type, len(offer.SDP))

	// WebRTC: register H264 codec explicitly into the MediaEngine
	m := webrtc.MediaEngine{}
	// register default codecs (VP8, etc.) then ensure H264 codec available for browsers
	if err := m.RegisterDefaultCodecs(); err != nil {
		http.Error(w, "RegisterDefaultCodecs failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Add common H264 profile-level fmtp (browsers usually accept this)
	h264 := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			Channels:     0,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			RTCPFeedback: nil,
		},
		PayloadType: 0,
	}
	if err := m.RegisterCodec(h264, webrtc.RTPCodecTypeVideo); err != nil {
		// non-fatal: continue but log
		log.Printf("warning: RegisterCodec(H264) returned: %v", err)
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(&m))

	// Create PeerConnection
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		http.Error(w, "NewPeerConnection error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Important: add transceiver BEFORE setting remote description so browser includes m=video
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendonly,
	}); err != nil {
		http.Error(w, "AddTransceiverFromKind error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create a local H264 track. We're assuming the camera gives H264 NALs.
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"video", "pion",
	)
	if err != nil {
		http.Error(w, "NewTrackLocalStaticSample error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := pc.AddTrack(videoTrack); err != nil {
		http.Error(w, "AddTrack error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Set remote description (the offer)
	if err := pc.SetRemoteDescription(offer); err != nil {
		http.Error(w, "SetRemoteDescription error: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Create answer and set as local description
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		http.Error(w, "CreateAnswer error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		http.Error(w, "SetLocalDescription error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Wait for ICE gathering to complete before returning the final answer
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	<-gatherComplete

	// Send the local description back
	respBytes, err := json.Marshal(pc.LocalDescription())
	if err != nil {
		http.Error(w, "Marshal answer error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(respBytes)

	// Now start pushing camera frames into the track in a goroutine
	go func() {
		// If device.GetOutput closes on device.Stop, this loop will exit.
		for frame := range dev.GetOutput() {
			// frame.Data should contain H264 NALs if camera was opened with H264
			if len(frame) == 0 {
				continue
			}

			// Write sample; Duration approximates 30fps
			err := videoTrack.WriteSample(media.Sample{
				Data:     frame,
				Duration: time.Second / 30,
			})
			if err != nil {
				log.Printf("videoTrack WriteSample error: %v", err)
				// If WriteSample fails, break to avoid tight error loop
				return
			}
		}
	}()
}
