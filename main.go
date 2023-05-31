package main

import (
	"fmt"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"
	"io"
	"log"
	"time"

	"github.com/Jeffail/gabs/v2"
	"github.com/deepgram-devs/go-sdk/deepgram"
	"github.com/gorilla/websocket"
)

var (
	livekitUrl       = "wss://deepgramtest.livekit.cloud"
	livekitapiKey    = ""
	livekitapiSecret = ""
	livekitroom      = "myroom"
	livekitidentity  = "osman-deepgram"
	deepgramapikey   = ""
	room             *lksdk.Room
)

type Transcriber struct {
	oggWriter     *io.PipeWriter
	oggReader     *io.PipeReader
	oggSerializer *oggwriter.OggWriter
}

func getJoinToken() (string, error) {
	at := auth.NewAccessToken(livekitapiKey, livekitapiSecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     livekitroom,
	}
	at.AddGrant(grant).
		SetIdentity(livekitidentity).
		SetValidFor(time.Hour)

	return at.ToJWT()
}

func trackPublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if publication.Source() != livekit.TrackSource_MICROPHONE {
		return
	}

	err := publication.SetSubscribed(true)
	if err != nil {
		logger.Errorw("failed to subscribe to the track", err, "track", publication.SID(), "participant", rp.SID())
		return
	}
}

func (t *Transcriber) WriteRTP(pkt *rtp.Packet, track *webrtc.TrackRemote) error {
	if t.oggSerializer == nil {
		oggSerializer, err := oggwriter.NewWith(t.oggWriter, track.Codec().ClockRate, track.Codec().Channels)
		if err != nil {
			logger.Errorw("failed to create ogg serializer", err)
			return err
		}
		t.oggSerializer = oggSerializer
	}

	if err := t.oggSerializer.WriteRTP(pkt); err != nil {
		return err
	}

	return nil
}

func (t *Transcriber) start() error {
	dg := *deepgram.NewClient(deepgramapikey)
	options := deepgram.LiveTranscriptionOptions{
		Language:  "en-US",
		Punctuate: true,
	}

	dgConn, _, err := dg.LiveTranscription(options)
	if err != nil {
		fmt.Println("ERROR reading message")
		log.Println(err)
	}

	//read
	go func() {
		for {
			_, message, err := dgConn.ReadMessage()
			if err != nil {
				fmt.Println("ERROR reading message")
				log.Fatal(err)
			}

			jsonParsed, jsonErr := gabs.ParseJSON(message)
			if jsonErr != nil {
				log.Fatal(err)
			}
			log.Printf("recv: %s", jsonParsed.Path("channel.alternatives.0.transcript").String())

		}
	}()

	for {
		endStreamCh := make(chan struct{})
		nextCh := make(chan struct{})

		// Forward oggreader to the speech stream
		go func() {
			defer close(nextCh)
			buf := make([]byte, 1024)
			for {
				select {
				case <-endStreamCh:
					return
				default:
					n, err := t.oggReader.Read(buf)
					if err != nil {
						if err != io.EOF {
							logger.Errorw("failed to read from ogg reader", err)
						}
						return
					}

					if n <= 0 {
						continue // No data
					}

					dgConn.WriteMessage(websocket.BinaryMessage, buf[:n])
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()

		close(endStreamCh)
		<-nextCh

	}
}

func trackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	oggReader, oggWriter := io.Pipe()
	t := &Transcriber{
		oggReader: oggReader,
		oggWriter: oggWriter,
	}

	go t.start()

	go func() {
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				if err != io.EOF {
					logger.Errorw("failed to read track", err, "participant", rp.SID())
				}
				return
			}

			err = t.WriteRTP(pkt, track)
			if err != nil {
				if err != io.EOF {
					logger.Errorw("failed to forward pkt to the transcriber", err, "participant", rp.SID())
				}
				return
			} else {
				logger.Errorw("failed to write track", err)
			}
		}
	}()
}

func main() {
	token, _ := getJoinToken()

	roomCallback := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackPublished:  trackPublished,
			OnTrackSubscribed: trackSubscribed,
		},
	}

	room, _ = lksdk.ConnectToRoomWithToken(livekitUrl, token, roomCallback, lksdk.WithAutoSubscribe(false))

	for {

	}
}
