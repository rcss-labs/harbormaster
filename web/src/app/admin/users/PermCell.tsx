import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { PermissionsDTO, TagColor } from '../../../api/types'
import { Popover } from '../../../components/Popover'
import { TagChip } from '../../../components/TagChip'
import { usePhone } from '../../../components/usePhone'
import { Toggle } from '../../../components/Toggle'
import { useToast } from '../../../components/Toast'
import { ACTIONS, PRESETS, bitOf, normalize, presetOf, toggle } from '../perms'
import styles from './users.module.css'

interface PermCellProps {
  userId: number
  username: string
  tagId: number
  tagName: string
  actions: number
  /** Draws the tag as a coloured chip on the phone row. */
  tagColor?: TagColor
}

/** One button per user and tag; its popover holds a preset select and six action toggles. Edits apply optimistically and roll back on error. */
export function PermCell({ userId, username, tagId, tagName, tagColor, actions }: PermCellProps) {
  const phone = usePhone()
  const queryClient = useQueryClient()
  const toast = useToast()
  const [open, setOpen] = useState(false)

  const save = useMutation({
    mutationFn: (next: number) => api.put<{ actions: number }>(`/api/permissions/${userId}/${tagId}`, { actions: next }),
    onMutate: async (next) => {
      await queryClient.cancelQueries({ queryKey: ['permissions'] })
      const previous = queryClient.getQueryData<PermissionsDTO>(['permissions'])
      queryClient.setQueryData<PermissionsDTO>(['permissions'], (current) =>
        current && {
          ...current,
          users: current.users.map((user) =>
            user.id === userId ? { ...user, perms: { ...user.perms, [String(tagId)]: next } } : user,
          ),
        },
      )
      return { previous }
    },
    onError: (error, _next, context) => {
      queryClient.setQueryData(['permissions'], context?.previous)
      toast.show(error.message, 'bad')
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['permissions'] })
      void queryClient.invalidateQueries({ queryKey: ['users'] })
    },
  })

  const preset = presetOf(actions)
  const letters = ACTIONS.filter((_, index) => (actions & bitOf(index)) !== 0)
    .map(([, , letter]) => letter)
    .join('')
  return (
    <div className={styles.pcell}>
      <button
        type="button"
        className={`${styles.pbtn} ${phone ? styles.pbtnRow : ''}`.trim()}
        aria-label={`${username} on ${tagName}: ${preset}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
      >
        {phone && tagColor && <TagChip tag={{ id: tagId, name: tagName, color: tagColor, inherited: false }} />}
        <span>{preset}</span>
        {letters && <span className={styles.mono}>{letters}</span>}
      </button>
      <Popover open={open} onClose={() => setOpen(false)} label={`Permissions for ${username} on ${tagName}`} width={260}>
        <div className={styles.ppop}>
          <select
            className={styles.ctl}
            aria-label={`Preset for ${username} on ${tagName}`}
            value={preset}
            onChange={(event) => save.mutate(normalize(PRESETS[event.target.value]))}
          >
            {preset === 'Custom' && <option value="Custom">Custom</option>}
            {Object.keys(PRESETS).map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
          {ACTIONS.map(([key, name], index) => (
            <Toggle key={key} checked={(actions & bitOf(index)) !== 0} onChange={() => save.mutate(toggle(actions, bitOf(index)))}>
              {name}
            </Toggle>
          ))}
          <p className={styles.hint}>Any action also turns on View. Turning View off clears the rest.</p>
        </div>
      </Popover>
    </div>
  )
}
