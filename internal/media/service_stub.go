//go:build !linux && !windows && (!darwin || !cgo)

package media

import "time"

// Service is an unavailable media session on unsupported builds.
type Service struct{}

// New returns no service on unsupported builds.
func New(_ func(Command)) (*Service, error) { return nil, nil }

// Run executes work without a platform event loop.
func Run(_ *Service, work func() error) error { return work() }

// Update has no effect on unsupported builds.
func (s *Service) Update(_ State) {}

// Seeked has no effect on unsupported builds.
func (s *Service) Seeked(_ time.Duration) {}

// Close has no effect on unsupported builds.
func (s *Service) Close() {}
