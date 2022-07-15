package output

import (
	"context"
	"os"
	"strings"
	"sync"

	"cloud.google.com/go/pubsub"
	"github.com/devopsext/events/common"
	sreCommon "github.com/devopsext/sre/common"
	toolsRender "github.com/devopsext/tools/render"
	"github.com/devopsext/utils"
	"google.golang.org/api/option"
)

type PubSubOutputOptions struct {
	Credentials   string
	ProjectID     string
	Message       string
	TopicSelector string
}

type PubSubOutput struct {
	wg       *sync.WaitGroup
	client   *pubsub.Client
	ctx      context.Context
	message  *toolsRender.TextTemplate
	selector *toolsRender.TextTemplate
	options  PubSubOutputOptions
	tracer   sreCommon.Tracer
	logger   sreCommon.Logger
	requests sreCommon.Counter
	errors   sreCommon.Counter
}

func (ps *PubSubOutput) Name() string {
	return "PubSub"
}

func (ps *PubSubOutput) Send(event *common.Event) {

	ps.wg.Add(1)
	go func() {
		defer ps.wg.Done()

		if event == nil {
			ps.logger.Debug("Event is empty")
			return
		}

		span := ps.tracer.StartFollowSpan(event.GetSpanContext())
		defer span.Finish()

		if event.Data == nil {
			ps.logger.SpanError(span, "Event data is empty")
			return
		}

		jsonObject, err := event.JsonObject()
		if err != nil {
			ps.logger.SpanError(span, err)
			return
		}

		topics := ""
		if ps.selector != nil {
			b, err := ps.selector.RenderObject(jsonObject)
			if err != nil {
				ps.logger.SpanDebug(span, err)
			} else {
				topics = string(b)
			}
		}

		if utils.IsEmpty(topics) {
			ps.logger.SpanError(span, "PubSub topics are not found")
			return
		}

		b, err := ps.message.RenderObject(jsonObject)
		if err != nil {
			ps.logger.SpanError(span, err)
			return
		}

		message := strings.TrimSpace(string(b))
		if utils.IsEmpty(message) {
			ps.logger.SpanDebug(span, "PubSub message is empty")
			return
		}

		ps.logger.SpanDebug(span, "PubSub message => %s", message)

		arr := strings.Split(topics, "\n")
		for _, topic := range arr {
			topic = strings.TrimSpace(topic)
			if utils.IsEmpty(topic) {
				continue
			}

			ps.requests.Inc(topic)

			t := ps.client.Topic(topic)
			serverID, err := t.Publish(ps.ctx, &pubsub.Message{Data: []byte(message)}).Get(ps.ctx)
			if err != nil {
				ps.errors.Inc(topic)
				ps.logger.SpanError(span, err)
				continue
			}
			ps.logger.SpanDebug(span, "PubSub server ID => %s", serverID)
		}
	}()
}

func NewPubSubOutput(wg *sync.WaitGroup,
	options PubSubOutputOptions,
	templateOptions toolsRender.TemplateOptions,
	observability *common.Observability) *PubSubOutput {
	logger := observability.Logs()
	if utils.IsEmpty(options.Credentials) || utils.IsEmpty(options.ProjectID) {
		logger.Debug("PubSub output credentials or project ID is not defined. Skipped")
		return nil
	}

	var o option.ClientOption
	if _, err := os.Stat(options.Credentials); err == nil {
		o = option.WithCredentialsFile(options.Credentials)
	} else {
		o = option.WithCredentialsJSON([]byte(options.Credentials))
	}

	ctx := context.Background()
	client, err := pubsub.NewClient(ctx, options.ProjectID, o)
	if err != nil {
		logger.Error(err)
		return nil
	}

	messageOpts := toolsRender.TemplateOptions{
		Name:       "pubsub-message",
		Content:    common.Content(options.Message),
		TimeFormat: templateOptions.TimeFormat,
	}
	message, err := toolsRender.NewTextTemplate(messageOpts)
	if err != nil {
		logger.Error(err)
		return nil
	}

	selectorOpts := toolsRender.TemplateOptions{
		Name:       "pubsub-selector",
		Content:    common.Content(options.TopicSelector),
		TimeFormat: templateOptions.TimeFormat,
	}
	selector, err := toolsRender.NewTextTemplate(selectorOpts)
	if err != nil {
		logger.Error(err)
	}

	return &PubSubOutput{
		wg:       wg,
		client:   client,
		ctx:      ctx,
		message:  message,
		selector: selector,
		options:  options,
		logger:   logger,
		tracer:   observability.Traces(),
		requests: observability.Metrics().Counter("requests", "Count of all pubsub requests", []string{"topic"}, "pubsub", "output"),
		errors:   observability.Metrics().Counter("errors", "Count of all pubsub errors", []string{"topic"}, "pubsub", "output"),
	}
}
