//go:build integration

package tests

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

const streamRecvTimeout = 500 * time.Millisecond

var (
	errStreamRecvTimeout = errors.New("stream Recv timed out")

	errFakeDriver = errors.New("fake driver: simulated failure")
)

// ---------------------------------------------------------------------------
// mTLS handshake
// ---------------------------------------------------------------------------

type DaemonGRPCAuthSuite struct{ suite.Suite }

func TestDaemonGRPCAuthSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(DaemonGRPCAuthSuite))
}

func (s *DaemonGRPCAuthSuite) TestHappyMTLS() {
	harness := newDaemon(s.T())

	resp, err := s.callStartCycle(harness, harness.PKI.ClientTLS)

	s.Require().NoError(err)
	s.Require().NotNil(resp)
	s.NotEmpty(resp.GetInjectToken())
	s.NotNil(resp.GetExpireTime())
}

func (s *DaemonGRPCAuthSuite) TestNoClientCert() {
	harness := newDaemon(s.T())

	_, err := s.callStartCycle(harness, harness.PKI.NoClientCertTLS)
	s.Require().Error(err)
}

func (s *DaemonGRPCAuthSuite) TestClientCertFromForeignCA() {
	harness := newDaemon(s.T())

	_, err := s.callStartCycle(harness, harness.PKI.BadClientTLS)
	s.Require().Error(err)
}

func (s *DaemonGRPCAuthSuite) TestWrongServerName() {
	harness := newDaemon(s.T())

	rogue := harness.PKI.ClientTLS.Clone()
	rogue.ServerName = "evil.example.invalid"

	_, err := s.callStartCycle(harness, rogue)
	s.Require().Error(err)
}

func (s *DaemonGRPCAuthSuite) TestPlaintextClientHittingMTLSServer() {
	harness := newDaemon(s.T())

	_, err := s.callStartCycle(harness, nil)
	s.Require().Error(err)
	s.T().Logf("expected handshake failure: %v", err)
}

func (s *DaemonGRPCAuthSuite) callStartCycle(
	harness *daemonHarness,
	tlsCfg *tls.Config,
) (*a2textv1.StartCycleResponse, error) {
	conn := harness.Dial(s.T(), tlsCfg)
	client := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	return client.StartCycle(ctx, &a2textv1.StartCycleRequest{})
}

// ---------------------------------------------------------------------------
// Single-client transport guard
// ---------------------------------------------------------------------------

type DaemonGRPCGuardSuite struct{ suite.Suite }

func TestDaemonGRPCGuardSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(DaemonGRPCGuardSuite))
}

func (s *DaemonGRPCGuardSuite) TestSingleClientSerialRPCs() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)
	sec := a2textv1.NewSecretServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	startResp, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)
	s.NotEmpty(startResp.GetInjectToken())

	injectResp, err := kb.Inject(ctx, &a2textv1.InjectRequest{
		Text:        "hello",
		InjectToken: startResp.GetInjectToken(),
	})
	s.Require().NoError(err)
	s.EqualValues(4, injectResp.GetEventsWritten())
	s.Equal(1, harness.Driver.Calls())

	_, err = sec.Set(ctx, &a2textv1.SetSecretRequest{Key: "openai", Value: []byte("sk-test")})
	s.Require().NoError(err)

	listResp, err := sec.List(ctx, &a2textv1.ListSecretsRequest{})
	s.Require().NoError(err)
	s.Len(listResp.GetSecrets(), 1)
}

func (s *DaemonGRPCGuardSuite) TestSecondConnectionRejected() {
	harness := newDaemon(s.T())

	conn1 := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb1 := a2textv1.NewKeyboardServiceClient(conn1)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	_, err := kb1.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)

	conn2 := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb2 := a2textv1.NewKeyboardServiceClient(conn2)

	_, err = kb2.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().Error(err)
	s.Equal(codes.AlreadyExists, status.Code(err))
}

// TestOwnerReleasedOnDisconnect polls the second conn because the
// guard releases via gRPC stats.Handler ConnEnd, which fires
// asynchronously after the TCP close.
func (s *DaemonGRPCGuardSuite) TestOwnerReleasedOnDisconnect() {
	harness := newDaemon(s.T())

	conn1 := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb1 := a2textv1.NewKeyboardServiceClient(conn1)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	_, err := kb1.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)

	s.Require().NoError(conn1.Close())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn2 := harness.Dial(s.T(), harness.PKI.ClientTLS)
		kb2 := a2textv1.NewKeyboardServiceClient(conn2)

		_, probeErr := kb2.StartCycle(ctx, &a2textv1.StartCycleRequest{})

		code := status.Code(probeErr)
		if probeErr == nil || code == codes.FailedPrecondition {
			return
		}

		s.Require().Equal(codes.AlreadyExists, code, "unexpected: %v", probeErr)

		time.Sleep(20 * time.Millisecond)
	}

	s.FailNow("second client never promoted after first disconnect")
}

// ---------------------------------------------------------------------------
// StreamHotkeyEvents
// ---------------------------------------------------------------------------

type DaemonGRPCStreamSuite struct{ suite.Suite }

func TestDaemonGRPCStreamSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(DaemonGRPCStreamSuite))
}

func (s *DaemonGRPCStreamSuite) TestInitialStateFrame() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)

	streamCtx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	_, initial := s.openStream(conn, streamCtx)

	s.Equal(a2textv1.HotkeyState_HOTKEY_STATE_IDLE, initial.GetState())
	s.Equal(a2textv1.HotkeyMode_HOTKEY_MODE_HOLD, initial.GetMode())
	s.Empty(initial.GetActiveInjectToken())
}

func (s *DaemonGRPCStreamSuite) TestPressEventAfterStart() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)

	streamCtx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	stream, _ := s.openStream(conn, streamCtx)

	const token = "tok-press"
	s.Require().NoError(harness.Hub.Start(streamCtx, token))

	frame, err := recvNextFrame(stream)
	s.Require().NoError(err)

	event := frame.GetEvent()
	s.Require().NotNil(event)
	s.Equal(a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_PRESS, event.GetKind())
	s.Equal(token, event.GetInjectToken())
}

func (s *DaemonGRPCStreamSuite) TestReleaseEventAfterEnd() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)

	streamCtx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	stream, _ := s.openStream(conn, streamCtx)

	const token = "tok-release"
	s.Require().NoError(harness.Hub.Start(streamCtx, token))

	_, err := recvNextFrame(stream)
	s.Require().NoError(err)

	harness.Hub.End()

	frame, err := recvNextFrame(stream)
	s.Require().NoError(err)

	event := frame.GetEvent()
	s.Require().NotNil(event)
	s.Equal(a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_RELEASE, event.GetKind())
	s.Equal(token, event.GetInjectToken())
}

func (s *DaemonGRPCStreamSuite) TestSequenceMonotonic() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)

	streamCtx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	stream, _ := s.openStream(conn, streamCtx)

	const cycles = 3

	sequences := make([]uint64, 0, cycles*2)

	for range cycles {
		s.Require().NoError(harness.Hub.Start(streamCtx, "tok"))

		pressFrame, err := recvNextFrame(stream)
		s.Require().NoError(err)

		sequences = append(sequences, pressFrame.GetEvent().GetSequence())

		harness.Hub.End()

		releaseFrame, err := recvNextFrame(stream)
		s.Require().NoError(err)

		sequences = append(sequences, releaseFrame.GetEvent().GetSequence())
	}

	s.Require().Len(sequences, cycles*2)

	for i := 1; i < len(sequences); i++ {
		s.Greaterf(sequences[i], sequences[i-1],
			"sequence must be monotonic: seq[%d]=%d, seq[%d]=%d",
			i, sequences[i], i-1, sequences[i-1])
	}
}

func (s *DaemonGRPCStreamSuite) TestSecondSubscribeRejected() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)

	streamCtx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	_, _ = s.openStream(conn, streamCtx)

	kb := a2textv1.NewKeyboardServiceClient(conn)
	second, err := kb.StreamHotkeyEvents(streamCtx, &a2textv1.StreamHotkeyEventsRequest{})
	s.Require().NoError(err)

	_, err = second.Recv()
	s.Require().Error(err)
	s.Equal(codes.FailedPrecondition, status.Code(err))
}

func (s *DaemonGRPCStreamSuite) TestResubscribeAfterCancel() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)

	firstCtx, firstCancel := context.WithTimeout(context.Background(), callTimeout)
	_, _ = s.openStream(conn, firstCtx)

	firstCancel()

	secondCtx, secondCancel := context.WithTimeout(context.Background(), callTimeout)
	defer secondCancel()

	kb := a2textv1.NewKeyboardServiceClient(conn)

	// Hub subscriber slot is released by a goroutine reading
	// ctx.Done — poll rather than sleep blindly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stream, err := kb.StreamHotkeyEvents(secondCtx, &a2textv1.StreamHotkeyEventsRequest{})
		s.Require().NoError(err)

		frame, recvErr := recvNextFrame(stream)
		if recvErr == nil && frame.GetInitialState() != nil {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	s.FailNow("resubscribe never succeeded after first stream cancel")
}

func (s *DaemonGRPCStreamSuite) TestServerShutdownClosesStream() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)

	streamCtx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	stream, _ := s.openStream(conn, streamCtx)

	harness.Close(s.T())

	_, err := recvNextFrame(stream)
	s.Require().Error(err)
	s.NotErrorIs(err, errStreamRecvTimeout)
}

func (s *DaemonGRPCStreamSuite) openStream(
	conn *grpc.ClientConn,
	streamCtx context.Context,
) (a2textv1.KeyboardService_StreamHotkeyEventsClient, *a2textv1.InitialState) {
	s.T().Helper()

	kb := a2textv1.NewKeyboardServiceClient(conn)

	stream, err := kb.StreamHotkeyEvents(streamCtx, &a2textv1.StreamHotkeyEventsRequest{})
	s.Require().NoError(err)

	frame, err := recvNextFrame(stream)
	s.Require().NoError(err)

	initial := frame.GetInitialState()
	s.Require().NotNil(initial)

	return stream, initial
}

// recvNextFrame is the bounded Recv used by every stream test.
//
// On the timeout branch the spawned goroutine keeps blocking on
// stream.Recv() until the underlying stream closes (server
// shutdown, ctx cancel from the caller, network reset). gRPC offers
// no API to abort a single Recv from outside the stream's own
// context, so this leak is intentional: each test process is
// short-lived and every leaked goroutine is collected when the
// harness Close path forcibly stops the gRPC server (Server.Close
// → grpc.Server.Stop → Recv returns with an error).
//
// The chan buffer is size 1 so the eventual Recv result has a
// landing slot and the goroutine can exit cleanly when the stream
// finally unblocks.
func recvNextFrame(
	stream a2textv1.KeyboardService_StreamHotkeyEventsClient,
) (*a2textv1.HotkeyStreamFrame, error) {
	type result struct {
		frame *a2textv1.HotkeyStreamFrame
		err   error
	}

	done := make(chan result, 1)

	go func() {
		f, err := stream.Recv()
		done <- result{frame: f, err: err}
	}()

	select {
	case r := <-done:
		return r.frame, r.err
	case <-time.After(streamRecvTimeout):
		return nil, errStreamRecvTimeout
	}
}

// ---------------------------------------------------------------------------
// Inject
// ---------------------------------------------------------------------------

type DaemonGRPCInjectSuite struct{ suite.Suite }

func TestDaemonGRPCInjectSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(DaemonGRPCInjectSuite))
}

func (s *DaemonGRPCInjectSuite) TestHappyPath() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	startResp, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)
	s.Require().NotEmpty(startResp.GetInjectToken())

	injectResp, err := kb.Inject(ctx, &a2textv1.InjectRequest{
		Text:        "hello world",
		InjectToken: startResp.GetInjectToken(),
	})
	s.Require().NoError(err)
	s.Equal(a2textv1.InjectMode_INJECT_MODE_PASTE, injectResp.GetMode())
	s.EqualValues(4, injectResp.GetEventsWritten())
	s.Equal(1, harness.Driver.Calls())
}

func (s *DaemonGRPCInjectSuite) TestEmptyTokenRejected() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	_, err := kb.Inject(ctx, &a2textv1.InjectRequest{
		Text:        "irrelevant",
		InjectToken: "",
	})
	s.Require().Error(err)
	s.Equal(codes.InvalidArgument, status.Code(err))
	s.Zero(harness.Driver.Calls())
}

func (s *DaemonGRPCInjectSuite) TestUnknownToken() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	_, err := kb.Inject(ctx, &a2textv1.InjectRequest{
		Text:        "irrelevant",
		InjectToken: "never-issued-token",
	})
	s.Require().Error(err)
	s.Equal(codes.PermissionDenied, status.Code(err))
	s.Zero(harness.Driver.Calls())
}

func (s *DaemonGRPCInjectSuite) TestConsumedTokenRejected() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	startResp, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)

	token := startResp.GetInjectToken()

	_, err = kb.Inject(ctx, &a2textv1.InjectRequest{Text: "first", InjectToken: token})
	s.Require().NoError(err)

	_, err = kb.Inject(ctx, &a2textv1.InjectRequest{Text: "replay", InjectToken: token})
	s.Require().Error(err)
	s.Equal(codes.PermissionDenied, status.Code(err))
	s.Equal(1, harness.Driver.Calls())
}

func (s *DaemonGRPCInjectSuite) TestExpiredToken() {
	const ttl = 50 * time.Millisecond

	harness := newDaemon(s.T(), withTokenTTL(ttl))

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	startResp, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)

	time.Sleep(ttl * 3)

	_, err = kb.Inject(ctx, &a2textv1.InjectRequest{
		Text:        "stale",
		InjectToken: startResp.GetInjectToken(),
	})
	s.Require().Error(err)
	s.Equal(codes.PermissionDenied, status.Code(err))
	s.Zero(harness.Driver.Calls())
}

func (s *DaemonGRPCInjectSuite) TestClipboardModeSkipsDriver() {
	harness := newDaemon(s.T(),
		withInjectMode(a2textv1.InjectMode_INJECT_MODE_CLIPBOARD),
	)

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	startResp, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)

	injectResp, err := kb.Inject(ctx, &a2textv1.InjectRequest{
		Text:        "irrelevant",
		InjectToken: startResp.GetInjectToken(),
	})
	s.Require().NoError(err)
	s.Equal(a2textv1.InjectMode_INJECT_MODE_CLIPBOARD, injectResp.GetMode())
	s.Zero(injectResp.GetEventsWritten())
	s.Zero(harness.Driver.Calls())
}

func (s *DaemonGRPCInjectSuite) TestDriverFailureMapsToInternal() {
	harness := newDaemon(s.T(), withDriverError(errFakeDriver))

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	startResp, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)

	_, err = kb.Inject(ctx, &a2textv1.InjectRequest{
		Text:        "fail",
		InjectToken: startResp.GetInjectToken(),
	})
	s.Require().Error(err)
	s.Equal(codes.Internal, status.Code(err))
	s.Equal(1, harness.Driver.Calls())
}

// ---------------------------------------------------------------------------
// StartCycle
// ---------------------------------------------------------------------------

type DaemonGRPCStartCycleSuite struct{ suite.Suite }

func TestDaemonGRPCStartCycleSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(DaemonGRPCStartCycleSuite))
}

func (s *DaemonGRPCStartCycleSuite) TestFreshTokenAndExpiry() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	before := time.Now()

	resp, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)
	s.Require().NotNil(resp)
	s.NotEmpty(resp.GetInjectToken())

	expire := resp.GetExpireTime().AsTime()
	s.True(expire.After(before),
		"expire_time (%s) must be after StartCycle call (%s)", expire, before)
}

func (s *DaemonGRPCStartCycleSuite) TestSecondStartCycleInFlight() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	_, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)

	_, err = kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().Error(err)
	s.Equal(codes.FailedPrecondition, status.Code(err))
}

func (s *DaemonGRPCStartCycleSuite) TestStartCycleAfterConsumeAndEnd() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	first, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)

	_, err = kb.Inject(ctx, &a2textv1.InjectRequest{
		Text:        "hello",
		InjectToken: first.GetInjectToken(),
	})
	s.Require().NoError(err)

	// Inject only frees the token slot; the Hub stays RECORDING
	// until something drives End. Production: evdev release edge.
	harness.Hub.End()

	second, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().NoError(err)
	s.NotEqual(first.GetInjectToken(), second.GetInjectToken())
}

// ---------------------------------------------------------------------------
// SecretService
// ---------------------------------------------------------------------------

type DaemonGRPCSecretSuite struct{ suite.Suite }

func TestDaemonGRPCSecretSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(DaemonGRPCSecretSuite))
}

func (s *DaemonGRPCSecretSuite) TestSetListRoundtrip() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	sec := a2textv1.NewSecretServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	_, err := sec.Set(ctx, &a2textv1.SetSecretRequest{
		Key:   "openai",
		Value: []byte("sk-openai-test"),
	})
	s.Require().NoError(err)

	_, err = sec.Set(ctx, &a2textv1.SetSecretRequest{
		Key:   "anthropic",
		Value: []byte("sk-anthropic-test"),
	})
	s.Require().NoError(err)

	listResp, err := sec.List(ctx, &a2textv1.ListSecretsRequest{})
	s.Require().NoError(err)
	s.Require().Len(listResp.GetSecrets(), 2)

	keys := make(map[string]bool, len(listResp.GetSecrets()))
	for _, meta := range listResp.GetSecrets() {
		keys[meta.GetKey()] = true
		s.NotNil(meta.GetStoreTime())
		s.False(meta.GetStoreTime().AsTime().IsZero())
	}

	s.True(keys["openai"])
	s.True(keys["anthropic"])
}

func (s *DaemonGRPCSecretSuite) TestEmptyKeyRejected() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	sec := a2textv1.NewSecretServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	_, err := sec.Set(ctx, &a2textv1.SetSecretRequest{
		Key:   "",
		Value: []byte("anything"),
	})
	s.Require().Error(err)
	s.Equal(codes.InvalidArgument, status.Code(err))
}

func (s *DaemonGRPCSecretSuite) TestEmptyValueRejected() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	sec := a2textv1.NewSecretServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	_, err := sec.Set(ctx, &a2textv1.SetSecretRequest{
		Key:   "openai",
		Value: nil,
	})
	s.Require().Error(err)
	s.Equal(codes.InvalidArgument, status.Code(err))
}

// TestBinaryValueRoundtripsExactly verifies the wire type is bytes
// rather than string — List only exposes metadata, so the exact
// bytes are checked via the store's internal Get helper.
func (s *DaemonGRPCSecretSuite) TestBinaryValueRoundtripsExactly() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	sec := a2textv1.NewSecretServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	binaryPayload := []byte{
		0x00, 0xFF, 0x42, 0x00, 0x80,
		0xC3, 0xA9, // "é" in UTF-8
		0xFE, 0xFF, // not a valid UTF-8 prefix
	}

	_, err := sec.Set(ctx, &a2textv1.SetSecretRequest{
		Key:   "binary",
		Value: binaryPayload,
	})
	s.Require().NoError(err)

	got, _, ok := harness.Secrets.Get("binary")
	s.Require().True(ok)
	s.Equal(binaryPayload, got)
}

// TestOverwriteBumpsStoreTime sleeps 2ms between writes so the
// store-time comparison is independent of monotonic-clock
// resolution on fast hosts.
func (s *DaemonGRPCSecretSuite) TestOverwriteBumpsStoreTime() {
	harness := newDaemon(s.T())

	conn := harness.Dial(s.T(), harness.PKI.ClientTLS)
	sec := a2textv1.NewSecretServiceClient(conn)

	ctx, cancel := callCtx(s.T(), harness)
	defer cancel()

	_, err := sec.Set(ctx, &a2textv1.SetSecretRequest{
		Key:   "openai",
		Value: []byte("v1"),
	})
	s.Require().NoError(err)

	listBefore, err := sec.List(ctx, &a2textv1.ListSecretsRequest{})
	s.Require().NoError(err)
	s.Require().Len(listBefore.GetSecrets(), 1)

	firstTime := listBefore.GetSecrets()[0].GetStoreTime().AsTime()

	time.Sleep(2 * time.Millisecond)

	_, err = sec.Set(ctx, &a2textv1.SetSecretRequest{
		Key:   "openai",
		Value: []byte("v2"),
	})
	s.Require().NoError(err)

	listAfter, err := sec.List(ctx, &a2textv1.ListSecretsRequest{})
	s.Require().NoError(err)
	s.Require().Len(listAfter.GetSecrets(), 1)

	secondTime := listAfter.GetSecrets()[0].GetStoreTime().AsTime()
	s.True(secondTime.After(firstTime),
		"overwrite must bump store_time: first=%s, second=%s", firstTime, secondTime)

	stored, _, ok := harness.Secrets.Get("openai")
	s.Require().True(ok)
	s.Equal([]byte("v2"), stored)
}

// ---------------------------------------------------------------------------
// Graceful shutdown
// ---------------------------------------------------------------------------

type DaemonGRPCShutdownSuite struct{ suite.Suite }

func TestDaemonGRPCShutdownSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(DaemonGRPCShutdownSuite))
}

const postShutdownDialTimeout = 500 * time.Millisecond

func (s *DaemonGRPCShutdownSuite) TestCloseReleasesServer() {
	harness := newDaemon(s.T())

	harness.Close(s.T())
	// Second Close must be a no-op (cancel field cleared).
	harness.Close(s.T())
}

func (s *DaemonGRPCShutdownSuite) TestRPCAfterShutdownFails() {
	harness := newDaemon(s.T())

	addr := harness.Addr
	tlsCfg := harness.PKI.ClientTLS

	harness.Close(s.T())

	conn := harness.Dial(s.T(), tlsCfg)
	kb := a2textv1.NewKeyboardServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), postShutdownDialTimeout)
	defer cancel()

	_, err := kb.StartCycle(ctx, &a2textv1.StartCycleRequest{})
	s.Require().Error(err)
	s.T().Logf("post-shutdown RPC against %s failed: %v", addr, err)

	code := status.Code(err)
	s.Truef(
		code == codes.Unavailable || code == codes.DeadlineExceeded,
		"expected Unavailable or DeadlineExceeded, got %v (err=%v)", code, err,
	)
}
