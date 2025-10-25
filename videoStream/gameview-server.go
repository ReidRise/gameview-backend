package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vladimirvivien/go4vl/device"
	"github.com/vladimirvivien/go4vl/v4l2"
)

var (
	frames <-chan []byte
)

// Upgrader is used to upgrade HTTP connections to WebSocket connections.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func imageServ(w http.ResponseWriter, req *http.Request) {
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}
	defer conn.Close()

	var frame []byte
	for frame = range frames {
		// encode & send...
		time.Sleep(33 * time.Millisecond) // ~30 fps
		err := conn.WriteMessage(websocket.BinaryMessage, frame)
		if err != nil {
			log.Println("Write error:", err)
			break
		}
	}
}

func gamepadHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade error:", err)
		return
	}
	defer conn.Close()

	file, err := os.OpenFile("/dev/hidg0", os.O_RDWR, 0644)
	if err != nil {
		log.Println("file error:", err)
		return
	}
	defer file.Close()

	for {
		_, msg, err := conn.ReadMessage()
		var hidReport []byte
		err = json.Unmarshal([]byte(msg), &hidReport)

		if err != nil {
			log.Println("read error:", err)
			break
		}

		fmt.Printf("%v\n", hidReport)
		// Write to file
		_, _ = file.Write(hidReport)
	}
}

func main() {
	port := ":9090"
	devName := "/dev/video0"
	flag.StringVar(&devName, "d", devName, "device name (path)")
	flag.StringVar(&port, "p", port, "webcam service port")

	camera, err := device.Open(
		devName,
		device.WithPixFormat(v4l2.PixFormat{PixelFormat: v4l2.PixelFmtMJPEG, Width: 1280, Height: 720}),
	)
	if err != nil {
		log.Fatalf("failed to open device: %s", err)
	}
	defer camera.Close()

	if err := camera.Start(context.TODO()); err != nil {
		log.Fatalf("camera start: %s", err)
	}

	frames = camera.GetOutput()

	log.Printf("Handling Gamepad\n")
	http.HandleFunc("/gamepad", gamepadHandler)

	log.Printf("Serving images: [%s/stream]\n", port)
	http.HandleFunc("/stream", imageServ)
	log.Fatal(http.ListenAndServe(port, nil))
}
