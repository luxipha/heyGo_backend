package repo

import (
	"context"
	"fmt"
	"sync"

	"github.com/cprakhar/uber-clone/services/trip-service/types"
	pbd "github.com/cprakhar/uber-clone/shared/proto/trip"
)

var (
	ErrNotFound = fmt.Errorf("resource not found")
)

type inMemoRepo struct {
	mu        sync.RWMutex
	trips     map[string]*types.TripModel
	rideFares map[string]*types.RideFareModel
}

type TripRepo interface {
	Create(ctx context.Context, trip *types.TripModel) (*types.TripModel, error)
	SaveRideFare(ctx context.Context, fare *types.RideFareModel) error
	GetRideFareByID(ctx context.Context, fareID string) (*types.RideFareModel, error)
	UpdateWithDriver(ctx context.Context, tripID string, driver *pbd.TripDriver) (*types.TripModel, error)
	Arrive(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error)
	Start(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error)
	GetByID(ctx context.Context, tripID string) (*types.TripModel, error)
	Complete(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error)
	Cancel(ctx context.Context, tripID, actorID, reason string) (*types.TripModel, bool, error)
	TrustParticipants(ctx context.Context, tripID string) ([]TrustSubject, error)
	Rate(ctx context.Context, tripID, actorID string, rating int, feedbackTags []string, comment string) (TrustSubject, bool, error)
}

type TrustSubject struct {
	CasperID string
	Role     string
}

func (r *inMemoRepo) Complete(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	trip, ok := r.trips[tripID]
	if !ok {
		return nil, false, ErrNotFound
	}
	if trip.Status == "completed" {
		return trip, false, nil
	}
	if trip.Status != "started" || trip.Driver == nil || trip.Driver.Id != actorID {
		return nil, false, fmt.Errorf("trip cannot be completed by this user")
	}
	trip.Status = "completed"
	return trip, true, nil
}

func (r *inMemoRepo) Cancel(ctx context.Context, tripID, actorID, reason string) (*types.TripModel, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	trip, ok := r.trips[tripID]
	if !ok {
		return nil, false, ErrNotFound
	}
	if trip.Status == "cancelled" {
		return trip, false, nil
	}
	if actorID != trip.RiderID && (trip.Driver == nil || actorID != trip.Driver.Id) {
		return nil, false, fmt.Errorf("trip cannot be cancelled by this user")
	}
	if trip.Status == "started" || trip.Status == "completed" {
		return nil, false, fmt.Errorf("trip cannot be cancelled from status %s", trip.Status)
	}
	trip.Status = "cancelled"
	return trip, true, nil
}

func (r *inMemoRepo) TrustParticipants(ctx context.Context, tripID string) ([]TrustSubject, error) {
	return nil, fmt.Errorf("trust participants unavailable in memory repository")
}
func (r *inMemoRepo) Rate(ctx context.Context, tripID, actorID string, rating int, feedbackTags []string, comment string) (TrustSubject, bool, error) {
	return TrustSubject{}, false, fmt.Errorf("ratings unavailable in memory repository")
}

// NewInMemoRepository creates a new instance of in-memory TripRepo
func NewInMemoRepository() *inMemoRepo {
	return &inMemoRepo{
		trips:     make(map[string]*types.TripModel),
		rideFares: make(map[string]*types.RideFareModel),
	}
}

func (r *inMemoRepo) GetByID(ctx context.Context, tripID string) (*types.TripModel, error) {
	r.mu.RLock()
	trip, exists := r.trips[tripID]
	r.mu.RUnlock()
	if !exists {
		return nil, ErrNotFound
	}
	return trip, nil
}

// Create adds a new trip to the in-memory store
func (r *inMemoRepo) Create(ctx context.Context, trip *types.TripModel) (*types.TripModel, error) {
	r.mu.Lock()
	r.trips[trip.ID] = trip
	r.mu.Unlock()
	return trip, nil
}

// SaveRideFare saves a ride fare to the in-memory store
func (r *inMemoRepo) SaveRideFare(ctx context.Context, fare *types.RideFareModel) error {
	r.mu.Lock()
	r.rideFares[fare.ID] = fare
	r.mu.Unlock()
	return nil
}

// GetRideFareByID retrieves a ride fare by its ID
func (r *inMemoRepo) GetRideFareByID(ctx context.Context, fareID string) (*types.RideFareModel, error) {
	r.mu.RLock()
	fare, exists := r.rideFares[fareID]
	r.mu.RUnlock()
	if !exists {
		return nil, ErrNotFound
	}
	return fare, nil
}

// UpdateWithDriver updates a trip with the given driver details and changes its status to "accepted"
func (r *inMemoRepo) UpdateWithDriver(ctx context.Context, tripID string, driver *pbd.TripDriver) (*types.TripModel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	trip, ok := r.trips[tripID]
	if !ok {
		return nil, ErrNotFound
	}
	if trip.Status == "pending" {
		trip.Driver = driver
		trip.Status = "accepted"
	}
	return trip, nil
}

func (r *inMemoRepo) Start(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	trip, ok := r.trips[tripID]
	if !ok {
		return nil, false, ErrNotFound
	}
	if trip.Driver == nil || trip.Driver.Id != actorID {
		return nil, false, fmt.Errorf("trip cannot be started by this user")
	}
	if trip.Status == "started" {
		return trip, false, nil
	}
	if trip.Status != "accepted" && trip.Status != "arrived" {
		return nil, false, fmt.Errorf("trip %s cannot start from status %s", tripID, trip.Status)
	}
	trip.Status = "started"
	return trip, true, nil
}

func (r *inMemoRepo) Arrive(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	trip, ok := r.trips[tripID]
	if !ok {
		return nil, false, ErrNotFound
	}
	if trip.Driver == nil || trip.Driver.Id != actorID {
		return nil, false, fmt.Errorf("trip cannot be marked arrived by this user")
	}
	if trip.Status == "arrived" {
		return trip, false, nil
	}
	if trip.Status != "accepted" {
		return nil, false, fmt.Errorf("trip %s cannot arrive from status %s", tripID, trip.Status)
	}
	trip.Status = "arrived"
	return trip, true, nil
}
