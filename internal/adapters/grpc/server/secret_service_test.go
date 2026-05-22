package server_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/partyzanex/a2text/internal/adapters/grpc/server"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// SecretServiceSetSuite covers the validation rules and the
// repository-error handling of SecretService.Set. The repository is
// always a mock so the suite stays a pure unit test — no file IO, no
// real time, no gRPC transport.
type SecretServiceSetSuite struct {
	suite.Suite

	ctrl *gomock.Controller
	repo *server.MockSecretRepository
	svc  *server.SecretService
}

// SetupTest builds a fresh mock + service per test case so test
// ordering and mock state never bleed across cases.
func (s *SecretServiceSetSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.repo = server.NewMockSecretRepository(s.ctrl)
	s.svc = server.NewSecretService(slog.New(slog.DiscardHandler), s.repo)
}

// TearDownTest finalises the mock controller and surfaces any
// unmet expectations as a test failure.
func (s *SecretServiceSetSuite) TearDownTest() {
	s.ctrl.Finish()
}

// TestSet_HappyPath verifies that a valid request triggers exactly
// one repository call and the response carries the timestamp the
// repository returned.
func (s *SecretServiceSetSuite) TestSet_HappyPath() {
	stored := time.Date(2026, time.May, 21, 12, 34, 56, 0, time.UTC)

	s.repo.EXPECT().
		Set(gomock.Any(), "openai", []byte("sk-test")).
		Return(stored, nil)

	resp, err := s.svc.Set(context.Background(), &a2textv1.SetSecretRequest{
		Key:   "openai",
		Value: []byte("sk-test"),
	})

	s.Require().NoError(err)
	s.Require().NotNil(resp)
	s.Require().NotNil(resp.GetStoreTime())
	s.Equal(stored.UTC(), resp.GetStoreTime().AsTime().UTC())
}

// TestSet_TrimsWhitespaceFromKey verifies that surrounding whitespace
// in the key is stripped before the repository is called.
func (s *SecretServiceSetSuite) TestSet_TrimsWhitespaceFromKey() {
	stored := time.Date(2026, time.May, 21, 0, 0, 0, 0, time.UTC)

	s.repo.EXPECT().
		Set(gomock.Any(), "deepgram", []byte("v")).
		Return(stored, nil)

	_, err := s.svc.Set(context.Background(), &a2textv1.SetSecretRequest{
		Key:   "  deepgram  ",
		Value: []byte("v"),
	})

	s.Require().NoError(err)
}

// TestSet_EmptyKey rejects an empty key with INVALID_ARGUMENT and
// must not touch the repository.
func (s *SecretServiceSetSuite) TestSet_EmptyKey() {
	resp, err := s.svc.Set(context.Background(), &a2textv1.SetSecretRequest{
		Key:   "",
		Value: []byte("v"),
	})

	s.requireGRPCError(err, codes.InvalidArgument)
	s.Nil(resp)
}

// TestSet_WhitespaceOnlyKey rejects whitespace-only keys after the
// trim step — same code as empty.
func (s *SecretServiceSetSuite) TestSet_WhitespaceOnlyKey() {
	resp, err := s.svc.Set(context.Background(), &a2textv1.SetSecretRequest{
		Key:   "   ",
		Value: []byte("v"),
	})

	s.requireGRPCError(err, codes.InvalidArgument)
	s.Nil(resp)
}

// TestSet_EmptyValue rejects an empty value with INVALID_ARGUMENT.
func (s *SecretServiceSetSuite) TestSet_EmptyValue() {
	resp, err := s.svc.Set(context.Background(), &a2textv1.SetSecretRequest{
		Key:   "openai",
		Value: nil,
	})

	s.requireGRPCError(err, codes.InvalidArgument)
	s.Nil(resp)
}

// TestSet_ZeroLengthValueAlsoEmpty rejects a non-nil but empty byte
// slice — both `nil` and `[]byte{}` should be treated identically.
func (s *SecretServiceSetSuite) TestSet_ZeroLengthValueAlsoEmpty() {
	resp, err := s.svc.Set(context.Background(), &a2textv1.SetSecretRequest{
		Key:   "openai",
		Value: []byte{},
	})

	s.requireGRPCError(err, codes.InvalidArgument)
	s.Nil(resp)
}

// TestSet_RepositoryError maps a repository failure to a gRPC
// Internal error without leaking the underlying error message on the
// wire.
func (s *SecretServiceSetSuite) TestSet_RepositoryError() {
	s.repo.EXPECT().
		Set(gomock.Any(), "openai", gomock.Any()).
		Return(time.Time{}, errors.New("disk full"))

	resp, err := s.svc.Set(context.Background(), &a2textv1.SetSecretRequest{
		Key:   "openai",
		Value: []byte("v"),
	})

	s.requireGRPCError(err, codes.Internal)
	s.Nil(resp)
}

// requireGRPCError asserts err is a gRPC status with the wanted
// code. Shared helper so each test case stays one assertion deep.
func (s *SecretServiceSetSuite) requireGRPCError(err error, want codes.Code) {
	s.T().Helper()
	s.Require().Error(err)

	st, ok := status.FromError(err)
	s.Require().True(ok, "error must be a gRPC status: %v", err)
	s.Equal(want, st.Code(), "status code mismatch (got=%s want=%s)", st.Code(), want)
}

// TestSecretServiceSetSuite is the standard testify entry point.
func TestSecretServiceSetSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(SecretServiceSetSuite))
}

// SecretServiceListSuite covers the response shaping and
// repository-error handling of SecretService.List. The repository is
// always a mock so the suite stays a pure unit test.
type SecretServiceListSuite struct {
	suite.Suite

	ctrl *gomock.Controller
	repo *server.MockSecretRepository
	svc  *server.SecretService
}

// SetupTest builds a fresh mock + service per test case so test
// ordering and mock state never bleed across cases.
func (s *SecretServiceListSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.repo = server.NewMockSecretRepository(s.ctrl)
	s.svc = server.NewSecretService(slog.New(slog.DiscardHandler), s.repo)
}

// TearDownTest finalises the mock controller and surfaces any
// unmet expectations as a test failure.
func (s *SecretServiceListSuite) TearDownTest() {
	s.ctrl.Finish()
}

// TestList_EmptyStore returns an empty Secrets slice when the
// repository has nothing to enumerate.
func (s *SecretServiceListSuite) TestList_EmptyStore() {
	s.repo.EXPECT().
		List(gomock.Any()).
		Return([]server.SecretRecord{}, nil)

	resp, err := s.svc.List(context.Background(), &a2textv1.ListSecretsRequest{})

	s.Require().NoError(err)
	s.Require().NotNil(resp)
	s.Empty(resp.GetSecrets())
}

// TestList_NilSliceIsEmpty treats a nil slice from the repository
// identically to an empty slice on the wire.
func (s *SecretServiceListSuite) TestList_NilSliceIsEmpty() {
	s.repo.EXPECT().
		List(gomock.Any()).
		Return(nil, nil)

	resp, err := s.svc.List(context.Background(), &a2textv1.ListSecretsRequest{})

	s.Require().NoError(err)
	s.Require().NotNil(resp)
	s.Empty(resp.GetSecrets())
}

// TestList_SingleRecord round-trips one record into one SecretMeta
// with matching key and timestamp.
func (s *SecretServiceListSuite) TestList_SingleRecord() {
	stored := time.Date(2026, time.May, 21, 10, 0, 0, 0, time.UTC)
	records := []server.SecretRecord{
		{Key: "openai", StoreTime: stored},
	}

	s.repo.EXPECT().
		List(gomock.Any()).
		Return(records, nil)

	resp, err := s.svc.List(context.Background(), &a2textv1.ListSecretsRequest{})

	s.Require().NoError(err)
	s.Require().Len(resp.GetSecrets(), 1)
	s.Equal("openai", resp.GetSecrets()[0].GetKey())
	s.Equal(stored.UTC(), resp.GetSecrets()[0].GetStoreTime().AsTime().UTC())
}

// TestList_MultipleRecords preserves the order the repository
// returned and converts each record into a SecretMeta.
func (s *SecretServiceListSuite) TestList_MultipleRecords() {
	t1 := time.Date(2026, time.May, 21, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, time.May, 21, 11, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, time.May, 21, 12, 0, 0, 0, time.UTC)

	records := []server.SecretRecord{
		{Key: "openai", StoreTime: t1},
		{Key: "deepgram", StoreTime: t2},
		{Key: "anthropic", StoreTime: t3},
	}

	s.repo.EXPECT().
		List(gomock.Any()).
		Return(records, nil)

	resp, err := s.svc.List(context.Background(), &a2textv1.ListSecretsRequest{})

	s.Require().NoError(err)
	s.Require().Len(resp.GetSecrets(), 3)
	s.Equal("openai", resp.GetSecrets()[0].GetKey())
	s.Equal(t1.UTC(), resp.GetSecrets()[0].GetStoreTime().AsTime().UTC())
	s.Equal("deepgram", resp.GetSecrets()[1].GetKey())
	s.Equal(t2.UTC(), resp.GetSecrets()[1].GetStoreTime().AsTime().UTC())
	s.Equal("anthropic", resp.GetSecrets()[2].GetKey())
	s.Equal(t3.UTC(), resp.GetSecrets()[2].GetStoreTime().AsTime().UTC())
}

// TestList_RepositoryError maps a repository failure to a gRPC
// Internal error without leaking the underlying error message on the
// wire.
func (s *SecretServiceListSuite) TestList_RepositoryError() {
	s.repo.EXPECT().
		List(gomock.Any()).
		Return(nil, errors.New("disk unreadable"))

	resp, err := s.svc.List(context.Background(), &a2textv1.ListSecretsRequest{})

	s.requireGRPCError(err, codes.Internal)
	s.Nil(resp)
}

// requireGRPCError mirrors the helper from SecretServiceSetSuite —
// kept method-receiver to stay one assertion deep per case.
func (s *SecretServiceListSuite) requireGRPCError(err error, want codes.Code) {
	s.T().Helper()
	s.Require().Error(err)

	st, ok := status.FromError(err)
	s.Require().True(ok, "error must be a gRPC status: %v", err)
	s.Equal(want, st.Code(), "status code mismatch (got=%s want=%s)", st.Code(), want)
}

// TestSecretServiceListSuite is the standard testify entry point for
// the List suite.
func TestSecretServiceListSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(SecretServiceListSuite))
}
