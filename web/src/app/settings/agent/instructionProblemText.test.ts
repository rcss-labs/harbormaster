import { describe, expect, it } from 'vitest'
import { instructionProblemText } from './instructionProblemText'

describe('instructionProblemText', () => {
  it.each([
    ['instructions_required', 'Write the instructions.'],
    ['instructions_too_long', 'Use at most 2000 characters.'],
    ['instructions_not_text', 'Remove the invisible or control characters. Pasting as plain text usually fixes this.'],
    ['instructions_hidden', 'Remove the invisible or control characters. Pasting as plain text usually fixes this.'],
    [
      'instructions_secret',
      'Part of this looks like a secret, such as a key, a password or a long random string. Remove it. Do not put a colon or = after words like auth, credentials, token or private. To point at a service with a random-looking name, use its tag or the scope.',
    ],
    ['instructions_not_allowed', 'The instructions were not accepted.'],
    ['something_new', 'The instructions were not accepted.'],
    ['', 'The instructions were not accepted.'],
  ])('%s', (code, text) => expect(instructionProblemText(code)).toBe(text))

  it('does not read a code from the object prototype', () => {
    expect(instructionProblemText('constructor')).toBe('The instructions were not accepted.')
  })
})
