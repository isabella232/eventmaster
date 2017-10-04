package eventmaster

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	context "golang.org/x/net/context"

	eventmaster "github.com/ContextLogic/eventmaster/proto"
)

// NewGRPCServer returns a populated
func NewGRPCServer(config *Flags, s *EventStore) *GRPCServer {
	return &GRPCServer{
		config: config,
		store:  s,
	}
}

// GRPCServer implements gRPC endpoints.
type GRPCServer struct {
	config *Flags
	store  *EventStore
}

func (s *GRPCServer) performOperation(method string, op func() (string, error)) (*eventmaster.WriteResponse, error) {
	start := time.Now()
	defer func() {
		grpcReqLatencies.WithLabelValues(method).Observe(msSince(start))
	}()

	id, err := op()
	if err != nil {
		grpcRespCounter.WithLabelValues(method, "1").Inc()
		fmt.Println("Error performing operation", method, err)
		return nil, errors.Wrapf(err, "operation %v", method)
	}

	grpcRespCounter.WithLabelValues(method, "0").Inc()
	return &eventmaster.WriteResponse{
		Id: id,
	}, nil
}

// AddEvent adds an event to the datastore.
func (s *GRPCServer) AddEvent(ctx context.Context, evt *eventmaster.Event) (*eventmaster.WriteResponse, error) {
	return s.performOperation("AddEvent", func() (string, error) {
		if evt.Data == nil {
			evt.Data = []byte("{}")
		}
		var data map[string]interface{}
		err := json.Unmarshal(evt.Data, &data)
		if err != nil {
			return "", errors.Wrap(err, "json decode of data")
		}
		return s.store.AddEvent(&UnaddedEvent{
			ParentEventID: evt.ParentEventId,
			EventTime:     evt.EventTime,
			DC:            evt.DC,
			TopicName:     evt.TopicName,
			Tags:          evt.TagSet,
			Host:          evt.Host,
			TargetHosts:   evt.TargetHostSet,
			User:          evt.User,
			Data:          data,
		})
	})
}

// GetEventByID returns an event by id.
func (s *GRPCServer) GetEventByID(ctx context.Context, id *eventmaster.EventID) (*eventmaster.Event, error) {
	name := "GetEventByID"
	start := time.Now()
	defer func() {
		grpcReqLatencies.WithLabelValues(name).Observe(msSince(start))
	}()

	ev, err := s.store.FindByID(id.EventID)
	if err != nil {
		grpcRespCounter.WithLabelValues(name, "1").Inc()
		fmt.Println("Error performing event store find", err)
		return nil, errors.Wrapf(err, "could not find by id", id.EventID)
	}
	d, err := json.Marshal(ev.Data)
	if err != nil {
		grpcRespCounter.WithLabelValues(name, "1").Inc()
		fmt.Println("Error marshalling event data into JSON", err)
		return nil, errors.Wrap(err, "data json marshal")
	}
	return &eventmaster.Event{
		EventId:       ev.EventID,
		ParentEventId: ev.ParentEventID,
		EventTime:     ev.EventTime,
		DC:            s.store.getDCName(ev.DCID),
		TopicName:     s.store.getTopicName(ev.TopicID),
		TagSet:        ev.Tags,
		Host:          ev.Host,
		TargetHostSet: ev.TargetHosts,
		User:          ev.User,
		Data:          d,
	}, nil
}

// GetEvents returns all Events.
func (s *GRPCServer) GetEvents(q *eventmaster.Query, stream eventmaster.EventMaster_GetEventsServer) error {
	name := "GetEvents"
	start := time.Now()
	defer func() {
		grpcReqLatencies.WithLabelValues(name).Observe(msSince(start))
	}()

	events, err := s.store.Find(q)
	if err != nil {
		grpcRespCounter.WithLabelValues(name, "1").Inc()
		fmt.Println("Error performing event store find", err)
		return errors.Wrapf(err, "unable to find %v", q)
	}
	for _, ev := range events {
		d, err := json.Marshal(ev.Data)
		if err != nil {
			grpcRespCounter.WithLabelValues(name, "1").Inc()
			fmt.Println("Error marshalling event data into JSON", err)
			return errors.Wrap(err, "json marshal of data")
		}
		if err := stream.Send(&eventmaster.Event{
			EventId:       ev.EventID,
			ParentEventId: ev.ParentEventID,
			EventTime:     ev.EventTime,
			DC:            s.store.getDCName(ev.DCID),
			TopicName:     s.store.getTopicName(ev.TopicID),
			TagSet:        ev.Tags,
			Host:          ev.Host,
			TargetHostSet: ev.TargetHosts,
			User:          ev.User,
			Data:          d,
		}); err != nil {
			grpcRespCounter.WithLabelValues(name, "1").Inc()
			fmt.Println("Error streaming event to grpc client", err)
			return errors.Wrap(err, "stream send")
		}
	}
	grpcRespCounter.WithLabelValues(name, "0").Inc()
	return nil
}

// GetEventIDs returns all event ids.
func (s *GRPCServer) GetEventIDs(q *eventmaster.TimeQuery, stream eventmaster.EventMaster_GetEventIDsServer) error {
	name := "GetEventByIDs"
	start := time.Now()
	defer func() {
		grpcReqLatencies.WithLabelValues(name).Observe(msSince(start))
	}()

	streamProxy := func(eventID string) error {
		return stream.Send(&eventmaster.EventID{EventID: eventID})
	}
	return s.store.FindIDs(q, streamProxy)
}

// AddTopic is the gRPC verison of AddTopic.
func (s *GRPCServer) AddTopic(ctx context.Context, t *eventmaster.Topic) (*eventmaster.WriteResponse, error) {
	return s.performOperation("AddTopic", func() (string, error) {
		if t.DataSchema == nil {
			t.DataSchema = []byte("{}")
		}
		var schema map[string]interface{}
		err := json.Unmarshal(t.DataSchema, &schema)
		if err != nil {
			return "", errors.Wrap(err, "json unmarshal of data schema")
		}
		return s.store.AddTopic(Topic{
			Name:   t.TopicName,
			Schema: schema,
		})
	})
}

// UpdateTopic is the gRPC version of updating a topic.
func (s *GRPCServer) UpdateTopic(ctx context.Context, t *eventmaster.UpdateTopicRequest) (*eventmaster.WriteResponse, error) {
	return s.performOperation("UpdateTopic", func() (string, error) {
		var schema map[string]interface{}
		err := json.Unmarshal(t.DataSchema, &schema)
		if err != nil {
			return "", errors.Wrap(err, "json unmarshal of data schema")
		}
		return s.store.UpdateTopic(t.OldName, Topic{
			Name:   t.NewName,
			Schema: schema,
		})
	})
}

// DeleteTopic is the gRPC version of DeleteTopic.
func (s *GRPCServer) DeleteTopic(ctx context.Context, t *eventmaster.DeleteTopicRequest) (*eventmaster.WriteResponse, error) {
	name := "DeleteTopic"
	start := time.Now()
	defer func() {
		grpcReqLatencies.WithLabelValues(name).Observe(msSince(start))
	}()

	err := s.store.DeleteTopic(t)
	if err != nil {
		grpcRespCounter.WithLabelValues(name, "1").Inc()
		fmt.Println("Error deleting topic: ", err)
		return nil, errors.Wrap(err, "delete topic")
	}
	grpcRespCounter.WithLabelValues(name, "0").Inc()
	return &eventmaster.WriteResponse{}, nil
}

// GetTopics is the gRPC call that returns all topics.
func (s *GRPCServer) GetTopics(ctx context.Context, _ *eventmaster.EmptyRequest) (*eventmaster.TopicResult, error) {
	name := "GetTopics"
	start := time.Now()
	defer func() {
		grpcReqLatencies.WithLabelValues(name).Observe(msSince(start))
	}()

	topics, err := s.store.GetTopics()
	if err != nil {
		grpcRespCounter.WithLabelValues(name, "1").Inc()
		fmt.Println("Error getting topics: ", err)
		return nil, errors.Wrap(err, "get topics")
	}

	var topicResults []*eventmaster.Topic

	for _, topic := range topics {
		var schemaBytes []byte
		if topic.Schema == nil {
			schemaBytes = []byte("{}")
		} else {
			schemaBytes, err = json.Marshal(topic.Schema)
			if err != nil {
				grpcRespCounter.WithLabelValues(name, "1").Inc()
				fmt.Println("Error marshalling topic schema: ", err)
				return nil, errors.Wrap(err, "json marshal of schema")
			}
		}
		topicResults = append(topicResults, &eventmaster.Topic{
			Id:         topic.ID,
			TopicName:  topic.Name,
			DataSchema: schemaBytes,
		})
	}
	grpcRespCounter.WithLabelValues(name, "0").Inc()
	return &eventmaster.TopicResult{
		Results: topicResults,
	}, nil
}

// AddDC is the gPRC version of adding a datacenter.
func (s *GRPCServer) AddDC(ctx context.Context, d *eventmaster.DC) (*eventmaster.WriteResponse, error) {
	return s.performOperation("AddDC", func() (string, error) {
		return s.store.AddDC(d)
	})
}

// UpdateDC is the gRPC version of updating a datacenter.
func (s *GRPCServer) UpdateDC(ctx context.Context, t *eventmaster.UpdateDCRequest) (*eventmaster.WriteResponse, error) {
	return s.performOperation("UpdateDC", func() (string, error) {
		return s.store.UpdateDC(t)
	})
}

// GetDCs is the gRPC version of getting all datacenters.
func (s *GRPCServer) GetDCs(ctx context.Context, _ *eventmaster.EmptyRequest) (*eventmaster.DCResult, error) {
	name := "GetDCs"
	start := time.Now()
	defer func() {
		grpcReqLatencies.WithLabelValues(name).Observe(msSince(start))
	}()

	dcs, err := s.store.GetDCs()
	if err != nil {
		grpcRespCounter.WithLabelValues(name, "1").Inc()
		fmt.Println("Error getting topics: ", err)
		return nil, errors.Wrap(err, "get dcs")
	}

	var dcResults []*eventmaster.DC

	for _, dc := range dcs {
		dcResults = append(dcResults, &eventmaster.DC{
			ID:     dc.ID,
			DCName: dc.Name,
		})
	}
	grpcRespCounter.WithLabelValues(name, "0").Inc()
	return &eventmaster.DCResult{
		Results: dcResults,
	}, nil
}

// Healthcheck is the gRPC health endpoint.
func (s *GRPCServer) Healthcheck(ctx context.Context, in *eventmaster.HealthcheckRequest) (*eventmaster.HealthcheckResponse, error) {
	return &eventmaster.HealthcheckResponse{Response: "OK"}, nil
}
