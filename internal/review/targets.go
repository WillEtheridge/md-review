package review

import (
	"encoding/json"
	"fmt"
)

// TargetFingerprints contains calculated thread and message fingerprints.
// Fingerprints describe exact raw sidecar object bytes and are never persisted.
type TargetFingerprints struct {
	Threads  map[string]string `json:"threads"`
	Messages map[string]string `json:"messages"`
}

func newTargetFingerprints() TargetFingerprints {
	return TargetFingerprints{
		Threads:  make(map[string]string),
		Messages: make(map[string]string),
	}
}

// These helpers are intentionally used only on a freshly decoded Document.
// Dirty nodes retain their pre-mutation raw slices; mutation responses instead
// decode the exact emitted bytes before calculating transport fingerprints.
func (document *Document) targetFingerprints() TargetFingerprints {
	result := newTargetFingerprints()
	for threadIndex, thread := range document.threads {
		threadNode := document.threadsNode.items[threadIndex]
		result.Threads[thread.ID] = Revision(threadNode.raw)

		messagesNode, _ := threadNode.get("messages")
		for messageIndex, message := range thread.Messages {
			result.Messages[message.ID] = Revision(messagesNode.items[messageIndex].raw)
		}
	}
	return result
}

func (document *Document) targetsForThread(threadID string) (TargetFingerprints, bool) {
	threadIndex, threadNode, ok := document.findThreadNode(threadID)
	if !ok {
		return TargetFingerprints{}, false
	}
	thread := document.threads[threadIndex]
	result := newTargetFingerprints()
	result.Threads[thread.ID] = Revision(threadNode.raw)

	messagesNode, _ := threadNode.get("messages")
	for messageIndex, message := range thread.Messages {
		result.Messages[message.ID] = Revision(messagesNode.items[messageIndex].raw)
	}
	return result, true
}

func (document *Document) threadFingerprint(threadID string) (string, bool) {
	_, threadNode, ok := document.findThreadNode(threadID)
	if !ok {
		return "", false
	}
	return Revision(threadNode.raw), true
}

func (document *Document) messageFingerprint(
	messageID string,
) (fingerprint string, threadID string, exists bool) {
	threadIndex, _, _, messageNode, ok := document.findMessageNode(messageID)
	if !ok {
		return "", "", false
	}
	return Revision(messageNode.raw), document.threads[threadIndex].ID, true
}

func (document *Document) findThreadNode(threadID string) (int, *value, bool) {
	for index := range document.threads {
		if document.threads[index].ID == threadID {
			return index, document.threadsNode.items[index], true
		}
	}
	return 0, nil, false
}

func (document *Document) findMessageNode(
	messageID string,
) (threadIndex int, messageIndex int, threadNode *value, messageNode *value, exists bool) {
	for currentThreadIndex, thread := range document.threads {
		for currentMessageIndex, message := range thread.Messages {
			if message.ID != messageID {
				continue
			}
			currentThreadNode := document.threadsNode.items[currentThreadIndex]
			messagesNode, _ := currentThreadNode.get("messages")
			return currentThreadIndex,
				currentMessageIndex,
				currentThreadNode,
				messagesNode.items[currentMessageIndex],
				true
		}
	}
	return 0, 0, nil, nil, false
}

func (document *Document) appendReply(threadID string, message Message) error {
	threadIndex, threadNode, ok := document.findThreadNode(threadID)
	if !ok {
		return fmt.Errorf("%w: thread %q does not exist", ErrInvalidOperation, threadID)
	}
	for _, thread := range document.threads {
		for _, existing := range thread.Messages {
			if existing.ID == message.ID {
				return fmt.Errorf(
					"%w: duplicate message ID %q",
					ErrInvalidOperation,
					message.ID,
				)
			}
		}
	}

	thread := cloneThread(document.threads[threadIndex])
	thread.Messages = append(thread.Messages, message)
	if thread.Status == StatusHandled || thread.Status == StatusResolved {
		thread.Status = StatusOpen
	}
	if err := validateThreadModel(thread); err != nil {
		return err
	}

	raw, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode reply: %w", err)
	}
	messageNode, err := parseJSON(raw)
	if err != nil {
		return fmt.Errorf("parse encoded reply: %w", err)
	}
	messageNode.markTreeDirty()
	messagesNode, _ := threadNode.get("messages")
	messagesNode.items = append(messagesNode.items, messageNode)
	messagesNode.markDirty()

	if thread.Status != document.threads[threadIndex].Status {
		if err := replaceObjectString(threadNode, "status", string(thread.Status)); err != nil {
			return err
		}
	}
	threadNode.markDirty()
	document.threadsNode.markDirty()
	document.root.markDirty()
	document.threads[threadIndex] = thread
	return nil
}

func (document *Document) editHumanMessage(
	messageID string,
	body string,
	editedAt string,
) (string, error) {
	threadIndex, messageIndex, threadNode, messageNode, ok := document.findMessageNode(messageID)
	if !ok {
		return "", fmt.Errorf("%w: message %q does not exist", ErrInvalidOperation, messageID)
	}
	current := document.threads[threadIndex].Messages[messageIndex]
	if current.Author.Type != "human" {
		return "", fmt.Errorf("%w: only human messages can be edited", ErrInvalidOperation)
	}

	updated := current
	updated.Body = body
	updated.EditedAt = &editedAt
	thread := cloneThread(document.threads[threadIndex])
	thread.Messages[messageIndex] = updated
	if err := validateThreadModel(thread); err != nil {
		return "", err
	}

	if err := replaceObjectString(messageNode, "body", body); err != nil {
		return "", err
	}
	if err := replaceObjectString(messageNode, "editedAt", editedAt); err != nil {
		return "", err
	}
	messageNode.markDirty()
	messagesNode, _ := threadNode.get("messages")
	messagesNode.markDirty()
	threadNode.markDirty()
	document.threadsNode.markDirty()
	document.root.markDirty()
	document.threads[threadIndex] = thread
	return thread.ID, nil
}

func (document *Document) changeThreadStatus(
	threadID string,
	status ThreadStatus,
) error {
	threadIndex, threadNode, ok := document.findThreadNode(threadID)
	if !ok {
		return fmt.Errorf("%w: thread %q does not exist", ErrInvalidOperation, threadID)
	}
	current := document.threads[threadIndex].Status
	allowed := (status == StatusResolved &&
		(current == StatusOpen || current == StatusHandled)) ||
		(status == StatusOpen && current == StatusResolved)
	if !allowed {
		return fmt.Errorf(
			"%w: cannot change thread status from %q to %q",
			ErrInvalidOperation,
			current,
			status,
		)
	}

	if err := replaceObjectString(threadNode, "status", string(status)); err != nil {
		return err
	}
	threadNode.markDirty()
	document.threadsNode.markDirty()
	document.root.markDirty()
	document.threads[threadIndex].Status = status
	return nil
}

func (document *Document) deleteUnrepliedThread(threadID string) error {
	threadIndex, _, ok := document.findThreadNode(threadID)
	if !ok {
		return fmt.Errorf("%w: thread %q does not exist", ErrInvalidOperation, threadID)
	}
	if len(document.threads[threadIndex].Messages) != 1 {
		return fmt.Errorf(
			"%w: a thread with replies cannot be deleted",
			ErrInvalidOperation,
		)
	}

	document.threadsNode.items = append(
		document.threadsNode.items[:threadIndex],
		document.threadsNode.items[threadIndex+1:]...,
	)
	document.threads = append(
		document.threads[:threadIndex],
		document.threads[threadIndex+1:]...,
	)
	document.threadsNode.markDirty()
	document.root.markDirty()
	return nil
}

func replaceObjectString(object *value, name string, replacement string) error {
	raw, err := json.Marshal(replacement)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	replacementNode, err := parseJSON(raw)
	if err != nil {
		return fmt.Errorf("parse encoded %s: %w", name, err)
	}
	for index := range object.members {
		if object.members[index].key == name {
			object.members[index].value = replacementNode
			return nil
		}
	}
	keyRaw, err := json.Marshal(name)
	if err != nil {
		return fmt.Errorf("encode member %s: %w", name, err)
	}
	object.members = append(object.members, member{
		key:    name,
		keyRaw: keyRaw,
		value:  replacementNode,
	})
	return nil
}
