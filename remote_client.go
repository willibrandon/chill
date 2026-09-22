package main

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
	"uuid"
)

const remoteResponseFrameLimit = 256 << 20

const remoteCommandHelp = `Usage: chill remote <command>

Commands:
  state                         print the complete runtime snapshot
  capabilities                  print methods, operations, topics, and limits
  call <operation> [options]    submit an operation; accepts --params JSON and --wait
  events <topic...>             stream subscribed events as newline-delimited JSON
  job <id>                      inspect an asynchronous job
  cancel <id>                   cancel a running job`

func sendRemoteRequestContext(ctx context.Context, request remoteRequest) (remoteResponse, error) {
	if request.Version == 0 {
		request.Version = remoteVersion
	}
	if request.ID == "" {
		request.ID = uuid.New().String()
	}
	conn, err := dialSocket()
	if err != nil {
		return remoteResponse{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(remoteWriteLimit))
	data, err := json.Marshal(request)
	if err != nil {
		return remoteResponse{}, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return remoteResponse{}, err
	}
	response, err := readRemoteResponse(bufio.NewReader(conn))
	if err != nil {
		return remoteResponse{}, err
	}
	if response.Version != remoteVersion {
		return remoteResponse{}, fmt.Errorf("daemon returned remote API version %d", response.Version)
	}
	if response.ID != request.ID {
		return remoteResponse{}, errors.New("daemon returned a mismatched request id")
	}
	if !response.OK {
		if response.Error == nil {
			return response, errors.New("remote request failed")
		}
		return response, fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	return response, nil
}

func readRemoteResponse(reader *bufio.Reader) (remoteResponse, error) {
	frame, err := readRemoteFrame(reader)
	if err != nil {
		return remoteResponse{}, err
	}
	var response remoteResponse
	if err := json.Unmarshal(frame, &response); err != nil {
		return remoteResponse{}, err
	}
	return response, nil
}

func sendRemoteRequest(request remoteRequest) (remoteResponse, error) {
	return sendRemoteRequestContext(context.Background(), request)
}

func submitRemoteOperation(ctx context.Context, operation string, params map[string]any, wait bool) (remoteResponse, error) {
	if err := ensureDaemon(); err != nil {
		return remoteResponse{}, err
	}
	response, err := sendRemoteRequestContext(ctx, remoteRequest{Method: "operation.submit", Operation: operation, Params: params})
	if err != nil || !wait || response.Job == nil {
		return response, err
	}
	jobID := response.Job.ID
	for {
		select {
		case <-ctx.Done():
			_, _ = sendRemoteRequest(remoteRequest{Method: "job.cancel", JobID: jobID})
			return remoteResponse{}, ctx.Err()
		case <-time.After(40 * time.Millisecond):
		}
		response, err = sendRemoteRequestContext(ctx, remoteRequest{Method: "job.get", JobID: jobID})
		if err != nil {
			return response, err
		}
		if response.Job != nil && terminalJobState(response.Job.State) {
			if response.Job.Error != nil {
				return response, fmt.Errorf("%s: %s", response.Job.Error.Code, response.Job.Error.Message)
			}
			return response, nil
		}
	}
}

func terminalJobState(state string) bool {
	return state == "succeeded" || state == "failed" || state == "canceled"
}

func runRemoteCommand(ctx context.Context, args []string) (string, error) {
	if helpRequested(args) {
		return remoteCommandHelp, nil
	}
	if len(args) == 0 {
		return "", errors.New("usage: chill remote <state|capabilities|call|events|job|cancel>")
	}
	switch args[0] {
	case "state":
		if len(args) != 1 {
			return "", errors.New("usage: chill remote state")
		}
		if !isDaemonRunning() {
			settings, settingsErr := loadPlaybackSettings()
			if settingsErr != nil {
				return "", settingsErr
			}
			library, libraryErr := loadLibrary()
			if libraryErr != nil {
				return "", libraryErr
			}
			podcasts, podcastErr := loadPodcastLibrary()
			if podcastErr != nil {
				return "", podcastErr
			}
			registry, providerErr := providers()
			if providerErr != nil {
				return "", providerErr
			}
			playback := Status{
				State: "stopped", Version: buildVersion(), BuildID: buildIdentity(), Protocol: daemonProtocol,
				QueueRevision: library.QueueRevision, Queue: cloneItems(library.Queue), PlayNext: cloneItems(library.PlayNext),
				Queued: len(library.Queue) + len(library.PlayNext), Shuffle: library.Shuffle, Repeat: library.Repeat,
				Volume: settings.Volume, Notifications: settings.Notifications, EQPreset: settings.EQPreset,
				EQBands: settings.equalizer().activeBands(),
				Audio:   settings.Audio.status(settings.Audio.Device),
			}
			interfaceState, interfaceErr := loadInterfaceSettings()
			if interfaceErr != nil {
				interfaceState = defaultInterfaceSettings()
			}
			snapshot := remoteSnapshot{Playback: playback, Library: library, Podcasts: podcasts, Providers: registry.list(ctx, false), Interface: interfaceState}
			return marshalRemoteOutput(remoteResponse{Version: remoteVersion, ID: "state", OK: true, Snapshot: &snapshot})
		}
		response, err := sendRemoteRequestContext(ctx, remoteRequest{ID: "state", Method: "state.get"})
		return marshalRemoteOutput(response, err)
	case "capabilities":
		if len(args) != 1 {
			return "", errors.New("usage: chill remote capabilities")
		}
		if err := ensureDaemon(); err != nil {
			return "", err
		}
		response, err := sendRemoteRequestContext(ctx, remoteRequest{ID: "capabilities", Method: "capabilities"})
		return marshalRemoteOutput(response, err)
	case "call":
		return runRemoteCall(ctx, args[1:])
	case "job":
		if len(args) != 2 {
			return "", errors.New("usage: chill remote job <id>")
		}
		response, err := sendRemoteRequestContext(ctx, remoteRequest{ID: "job", Method: "job.get", JobID: args[1]})
		return marshalRemoteOutput(response, err)
	case "cancel":
		if len(args) != 2 {
			return "", errors.New("usage: chill remote cancel <id>")
		}
		response, err := sendRemoteRequestContext(ctx, remoteRequest{ID: "cancel", Method: "job.cancel", JobID: args[1]})
		return marshalRemoteOutput(response, err)
	case "events":
		if len(args) < 2 {
			return "", errors.New("usage: chill remote events <topic...>")
		}
		err := streamRemoteEvents(ctx, args[1:], os.Stdout)
		if errors.Is(err, context.Canceled) {
			return "", nil
		}
		return "", err
	default:
		return "", fmt.Errorf("unknown remote command %q", args[0])
	}
}

func runRemoteCall(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("usage: chill remote call <operation> [--params JSON] [--wait]")
	}
	operation := args[0]
	params := map[string]any{}
	wait := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--wait":
			wait = true
		case "--params":
			if i+1 >= len(args) {
				return "", errors.New("--params needs a JSON object")
			}
			i++
			if err := json.Unmarshal([]byte(args[i]), &params); err != nil {
				return "", fmt.Errorf("invalid --params JSON: %w", err)
			}
		default:
			return "", fmt.Errorf("unknown remote call option %q", args[i])
		}
	}
	response, err := submitRemoteOperation(ctx, operation, params, wait)
	return marshalRemoteOutput(response, err)
}

func marshalRemoteOutput(response remoteResponse, errs ...error) (string, error) {
	if len(errs) > 0 && errs[0] != nil {
		return "", errs[0]
	}
	data, err := json.Marshal(response, json.Deterministic(true))
	return string(data), err
}

func streamRemoteEvents(ctx context.Context, topics []string, output io.Writer) error {
	if err := ensureDaemon(); err != nil {
		return err
	}
	conn, err := dialSocket()
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	request := remoteRequest{Version: remoteVersion, ID: "events", Method: "subscribe", Topics: topics}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(remoteWriteLimit))
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	reader := bufio.NewReader(conn)
	response, err := readRemoteResponse(reader)
	if err != nil {
		return err
	}
	if !response.OK {
		if response.Error != nil {
			return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
		}
		return errors.New("subscription failed")
	}
	for {
		line, err := readRemoteFrame(reader)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return ctx.Err()
			}
			return err
		}
		if _, err := output.Write(append(line, '\n')); err != nil {
			return err
		}
	}
}

func readRemoteFrame(reader *bufio.Reader) ([]byte, error) {
	frame := make([]byte, 0, 4096)
	for {
		part, continued, err := reader.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(frame)+len(part) > remoteResponseFrameLimit {
			return nil, fmt.Errorf("remote response exceeds %d bytes", remoteResponseFrameLimit)
		}
		frame = append(frame, part...)
		if !continued {
			return frame, nil
		}
	}
}
