// Command fakegateway stands in for the watcher's loopback gateway during
// the proof run (PROOF.md), so no production watcher is involved. It binds
// 127.0.0.1 only.
//
//	/          a page showing the MJPEG stream and live SSE lag
//	/v1/info   401, as the real gateway answers without a ticket
//	/mjpeg     multipart/x-mixed-replace JPEG frames at 5 fps
//	/sse       one server-sent event per second carrying the server time
//	/flv       a chunked byte stream, one 4 KiB chunk every 200 ms
//	/whep      POST echo with 201 and application/sdp, like WebRTC signalling
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"time"
)

func main() {
	port := flag.Int("port", 18445, "loopback port")
	dur := flag.Duration("stream", 15*time.Minute, "how long each stream runs")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	mux.HandleFunc("/v1/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"ticket required"}`)
	})
	mux.HandleFunc("/mjpeg", func(w http.ResponseWriter, r *http.Request) { mjpeg(w, r, *dur) })
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		tick(w, r, time.Second, *dur, func(i int) { fmt.Fprintf(w, "data: {\"n\":%d,\"t\":%d}\n\n", i, time.Now().UnixMilli()) })
	})
	mux.HandleFunc("/flv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		chunk := bytes.Repeat([]byte{0x46}, 4096)
		tick(w, r, 200*time.Millisecond, *dur, func(int) { w.Write(chunk) })
	})
	mux.HandleFunc("/whep", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var b bytes.Buffer
		b.ReadFrom(http.MaxBytesReader(w, r.Body, 64<<10))
		w.Header().Set("Content-Type", "application/sdp")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "v=0\r\na=answer-to:%d-bytes\r\n", b.Len())
	})
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("fakegateway on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, logReq(mux)))
}

func logReq(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s host=%q xfp=%q xfh=%q xff=%q", r.Method, r.URL.Path, r.Host,
			r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Forwarded-For"))
		h.ServeHTTP(w, r)
	})
}

// tick calls fn every d until the stream duration ends or the client goes.
func tick(w http.ResponseWriter, r *http.Request, d, total time.Duration, fn func(int)) {
	end := time.After(total)
	t := time.NewTicker(d)
	defer t.Stop()
	for i := 0; ; i++ {
		fn(i)
		w.(http.Flusher).Flush()
		select {
		case <-t.C:
		case <-end:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func mjpeg(w http.ResponseWriter, r *http.Request, total time.Duration) {
	mw := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+mw.Boundary())
	w.Header().Set("Cache-Control", "no-store")
	tick(w, r, 200*time.Millisecond, total, func(i int) {
		var b bytes.Buffer
		jpeg.Encode(&b, frame(i), &jpeg.Options{Quality: 70})
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", "image/jpeg")
		h.Set("Content-Length", fmt.Sprint(b.Len()))
		h.Set("X-Frame", fmt.Sprint(i))
		h.Set("X-Sent-At", fmt.Sprint(time.Now().UnixMilli()))
		pw, _ := mw.CreatePart(h)
		pw.Write(b.Bytes())
	})
}

// frame draws a moving bar whose position encodes the frame number, and a
// strip of 16 binary cells encoding it exactly.
func frame(i int) image.Image {
	const W, H = 320, 180
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	bg := color.RGBA{uint8(i * 7), 40, uint8(255 - i*3), 255}
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			img.Set(x, y, bg)
		}
	}
	bx := (i * 8) % W
	for y := 20; y < 140; y++ {
		for x := bx; x < bx+16 && x < W; x++ {
			img.Set(x, y, color.White)
		}
	}
	for bit := 0; bit < 16; bit++ {
		c := color.Black
		if i&(1<<bit) != 0 {
			c = color.White
		}
		for y := 150; y < 170; y++ {
			for x := bit * 20; x < bit*20+18; x++ {
				img.Set(x, y, c)
			}
		}
	}
	return img
}

const page = `<!doctype html><meta name=viewport content="width=device-width">
<title>spoolroom-tsnet proof</title>
<h3>MJPEG</h3><img src="/mjpeg" width=320 height=180>
<h3>SSE</h3><p id=s>waiting…</p>
<script>
let n=0,max=0;const es=new EventSource('/sse');
es.onmessage=e=>{const d=JSON.parse(e.data),lag=Date.now()-d.t;n++;max=Math.max(max,lag);
document.getElementById('s').textContent='events '+n+' · lag '+lag+' ms · max '+max+' ms (includes clock skew)';};
</script>`
