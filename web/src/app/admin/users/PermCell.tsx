import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../../api/client'
import type { PermissionsDTO } from '../../../api/types'
import { useToast } from '../../../components/Toast'
import { ACTIONS, PRESETS, bitOf, normalize, presetOf, toggle } from '../perms'
import styles from './users.module.css'

interface PermCellProps {
  userId: number
  username: string
  tagId: number
  tagName: string
  actions: number
}

/** Six action toggles plus a preset select for one user on one tag. Edits apply optimistically and roll back on error. */
export function PermCell({ userId, username, tagId, tagName, actions }: PermCellProps) {
  const queryClient = useQueryClient()
  const toast = useToast()

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
  return (
    <div className={styles.pcell}>
      <div className={styles.ptogs}>
        {ACTIONS.map(([key, name, letter], index) => {
          const on = (actions & bitOf(index)) !== 0
          return (
            <button
              key={key}
              type="button"
              className={`${styles.ptog} ${on ? styles.on : ''}`.trim()}
              title={name}
              aria-pressed={on}
              onClick={() => save.mutate(toggle(actions, bitOf(index)))}
            >
              {letter}
            </button>
          )
        })}
      </div>
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
    </div>
  )
}
