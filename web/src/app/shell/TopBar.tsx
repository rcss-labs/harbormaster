import { Fragment } from 'react'
import { Link } from 'react-router'
import { useCrumbsValue } from './crumbs'
import { Filters } from './Filters'
import { Search } from './Search'
import { ThemeToggle } from './ThemeToggle'
import styles from './shell.module.css'

export function TopBar() {
  const crumbs = useCrumbsValue()
  return (
    <header className={styles.topbar}>
      <nav className={styles.crumbs} aria-label="Breadcrumb">
        {crumbs.map(([label, href], index) =>
          index === crumbs.length - 1 || !href ? (
            <span key={index} className={styles.here}>
              {label}
            </span>
          ) : (
            <Fragment key={index}>
              <Link to={href}>{label}</Link>
              <span className={styles.sep}>/</span>
            </Fragment>
          ),
        )}
      </nav>
      <div className={styles.topTools}>
        <Filters />
        <Search />
        <ThemeToggle />
      </div>
    </header>
  )
}
