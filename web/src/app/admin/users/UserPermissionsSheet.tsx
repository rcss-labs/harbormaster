import type { PermissionsDTO, UserDTO } from '../../../api/types'
import { BottomSheet } from '../../../components/BottomSheet'
import { Pill } from '../../../components/Pill'
import { PermCell } from './PermCell'
import styles from './users.module.css'

interface UserPermissionsSheetProps {
  user: UserDTO | undefined
  permissions: PermissionsDTO | undefined
  onClose: () => void
}

/** Phone replacement for one row of the permission matrix: every tag with its preset, each opening the preset and toggle sheet. */
export function UserPermissionsSheet({ user, permissions, onClose }: UserPermissionsSheetProps) {
  if (!user) return null
  const row = permissions?.users.find((candidate) => candidate.id === user.id)
  return (
    <BottomSheet open onClose={onClose} label={`Edit permissions for ${user.username}`} title="Edit permissions" sub={user.username}>
      {user.admin ? (
        <Pill kind="info">Everything, on every tag</Pill>
      ) : !permissions || !row ? (
        <p className={styles.hint}>Loading...</p>
      ) : (
        <>
          <p className={styles.hint}>Choose a tag to set its preset or single actions.</p>
          <div className={styles.permList}>
            {permissions.tags.map((tag) => (
              <PermCell
                key={tag.id}
                userId={user.id}
                username={user.username}
                tagId={tag.id}
                tagName={tag.name}
                tagColor={tag.color}
                servicesInEffect={tag.services_in_effect}
                actions={row.perms[String(tag.id)] ?? 0}
              />
            ))}
          </div>
        </>
      )}
    </BottomSheet>
  )
}
