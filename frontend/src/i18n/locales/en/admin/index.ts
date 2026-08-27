import overview from './overview'
import channels from './channels'
import accounts from './accounts'
import resources from './resources'
import ops from './ops'
import settings from './settings'
import audit from './audit'
import promptAudit from './promptAudit'
import fingerprintObservation from './fingerprintObservation'
import plugins from './plugins'
import groupApplications from './groupApplications'

export default {
  ...overview,
  ...channels,
  ...accounts,
  ...resources,
  ...ops,
  ...settings,
  ...audit,
  ...promptAudit,
  ...fingerprintObservation,
  ...plugins,
  ...groupApplications,
}
