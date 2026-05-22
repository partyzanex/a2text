package server

//go:generate go run go.uber.org/mock/mockgen@latest -source=secret_service.go -destination=secret_service_mocks_test.go -package=server

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// SecretRepository is the persistence seam the SecretService uses to
// store and enumerate secrets. The infrastructure store implements
// this shape. Declared next to the consumer so the consumer owns the
// contract (DIP) per the project's architecture rules.
type SecretRepository interface {
	// Set writes (or overwrites) the value bound to key and returns
	// the wall-clock time it was persisted. Implementations must
	// treat the call as atomic from the caller's perspective.
	Set(ctx context.Context, key string, value []byte) (time.Time, error)

	// List returns metadata for every key currently in the store.
	// Values are never returned — only the key and its last-write
	// timestamp. Order is implementation-defined; the adapter does
	// not depend on any particular ordering.
	List(ctx context.Context) ([]SecretRecord, error)
}

// SecretRecord is the value-less metadata projection the repository
// returns from List. Lives in the adapter package because it is part
// of the consumer's contract; the infrastructure implementation
// imports it to match the interface.
type SecretRecord struct {
	// Key is the logical name of the stored credential.
	Key string

	// StoreTime is the wall-clock moment the current value was
	// persisted.
	StoreTime time.Time
}

// SecretService implements a2textv1.SecretServiceServer. It exposes
// the daemon-owned credential store over the gRPC channel.
type SecretService struct {
	a2textv1.UnimplementedSecretServiceServer

	log  *slog.Logger
	repo SecretRepository
}

// NewSecretService constructs a SecretService adapter. A nil log is
// replaced with a discard handler; repo is a required dependency —
// passing nil is a programmer error and will surface as a panic on
// first Set.
func NewSecretService(log *slog.Logger, repo SecretRepository) *SecretService {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &SecretService{
		log:  log,
		repo: repo,
	}
}

// Set persists the (key, value) pair via the repository and answers
// with the server-side store timestamp the UI can show as "last
// updated". Validation rules:
//
//   - key is required and is trimmed of leading/trailing whitespace
//     before the empty check.
//   - value is required (non-empty byte sequence).
//
// Expected gRPC error codes:
//
//   - INVALID_ARGUMENT — missing key or value.
//   - INTERNAL         — repository failed to persist.
func (s *SecretService) Set(
	ctx context.Context,
	req *a2textv1.SetSecretRequest,
) (*a2textv1.SetSecretResponse, error) {
	key := strings.TrimSpace(req.GetKey())
	if key == "" {
		return nil, status.Errorf(codes.InvalidArgument, "key must not be empty")
	}

	value := req.GetValue()
	if len(value) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "value must not be empty")
	}

	stored, err := s.repo.Set(ctx, key, value)
	if err != nil {
		s.log.Error("secret set failed",
			slog.String("key", key),
			slog.Any("error", err),
		)

		return nil, status.Errorf(codes.Internal, "set failed")
	}

	return &a2textv1.SetSecretResponse{
		StoreTime: timestamppb.New(stored),
	}, nil
}

// List enumerates the metadata of every stored credential. Values
// are never returned — callers receive only key names and their
// last-write timestamps.
//
// Expected gRPC error codes:
//
//   - INTERNAL — repository failed to enumerate.
func (s *SecretService) List(
	ctx context.Context,
	_ *a2textv1.ListSecretsRequest,
) (*a2textv1.ListSecretsResponse, error) {
	records, err := s.repo.List(ctx)
	if err != nil {
		s.log.Error("secret list failed",
			slog.Any("error", err),
		)

		return nil, status.Errorf(codes.Internal, "list failed")
	}

	out := make([]*a2textv1.SecretMeta, 0, len(records))

	for i := range records {
		out = append(out, &a2textv1.SecretMeta{
			Key:       records[i].Key,
			StoreTime: timestamppb.New(records[i].StoreTime),
		})
	}

	return &a2textv1.ListSecretsResponse{
		Secrets: out,
	}, nil
}
