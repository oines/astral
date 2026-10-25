import type { AstralInternalHarnessInputRequest as InputRequest } from '@astral/sdk'
import { randomID } from './browser'

type InputContent = Omit<InputRequest, 'id'>

export function sameInputContent(first: InputContent, second: InputContent) {
  return first.text === second.text
    && (first.mode || 'follow_up') === (second.mode || 'follow_up')
    && JSON.stringify(first.files || []) === JSON.stringify(second.files || [])
}

export async function prepareInputAttempt(previous: InputRequest | undefined, content: InputContent, reconcile: (id: string) => Promise<unknown>): Promise<InputRequest> {
  if (previous && sameInputContent(previous, content)) return previous
  // Wait for the old request's outcome before sending edited content with a
  // new identity. A failed lookup must leave the editable draft untouched.
  if (previous) await reconcile(previous.id)
  return { ...content, id: randomID() }
}
