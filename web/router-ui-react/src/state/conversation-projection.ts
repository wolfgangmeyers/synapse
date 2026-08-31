import { mergeMessages, messageKey, pruneHistoryMessages, reconcileHistoryMessages, type HistoryMessage } from "../domain/history";

export interface ConversationState { messages: HistoryMessage[]; highWaterSequence: number; selectedPeer?: string; followMessages: boolean }
export function appendConversation(state: ConversationState, message: HistoryMessage, now = Date.now()): ConversationState { return { ...state, messages: pruneHistoryMessages(mergeMessages(state.messages, [message]), now) }; }
export function reconcileConversation(state: ConversationState, snapshot: HistoryMessage[], highWaterSequence: number, now = Date.now()): ConversationState { return { ...state, highWaterSequence: Math.max(state.highWaterSequence, highWaterSequence), messages: pruneHistoryMessages(reconcileHistoryMessages(state.messages, snapshot, highWaterSequence), now) }; }
export function replacePendingConversation(state: ConversationState, localId: string, accepted: HistoryMessage): ConversationState { return appendConversation({ ...state, messages: state.messages.filter((message) => message.id !== localId) }, { ...accepted, direction: "outgoing" }); }
export function settlePendingConversation(state: ConversationState, localId: string, ok: boolean): ConversationState { return { ...state, messages: state.messages.map((message) => message.id === localId ? { ...message, pending: false, failed: !ok } : message) }; }
export function addressedPeer(message: HistoryMessage, ownerId: string): string | undefined { if (message.from === ownerId) return message.to; if (message.to === ownerId) return message.from; return undefined; }
export function shouldFollowToBottom(scrollTop: number, clientHeight: number, scrollHeight: number, threshold = 80): boolean { return scrollHeight - (scrollTop + clientHeight) <= threshold; }
export function shouldRequestHistory(selectedPeer: string | undefined): selectedPeer is string { return Boolean(selectedPeer); }
export { messageKey, mergeMessages, pruneHistoryMessages, reconcileHistoryMessages };
