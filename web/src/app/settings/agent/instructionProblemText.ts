const HIDDEN_TEXT = 'Remove the invisible or control characters. Pasting as plain text usually fixes this.'

const PROBLEM_TEXT: Record<string, string> = {
  instructions_required: 'Write the instructions.',
  instructions_too_long: 'Use at most 2000 characters.',
  instructions_not_text: HIDDEN_TEXT,
  instructions_hidden: HIDDEN_TEXT,
  instructions_secret:
    'Part of this looks like a secret, such as a key, a password or a long random string. Remove it. Do not put a colon or = after words like auth, credentials, token or private. To point at a service with a random-looking name, use its tag or the scope.',
}

const NOT_ACCEPTED = 'The instructions were not accepted.'

/** The one sentence for a problem code of the instructions; a code this version does not know reads as not accepted. */
export const instructionProblemText = (code: string): string => (Object.hasOwn(PROBLEM_TEXT, code) ? PROBLEM_TEXT[code] : NOT_ACCEPTED)
