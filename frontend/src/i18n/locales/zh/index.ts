import landing from './landing'
import common from './common'
import dashboard from './dashboard'
import channelMonitorV2 from './channelMonitorV2'
import batchImage from './batchImage'
import admin from './admin'
import misc from './misc'
import { candyTestsZh } from '../candyTests'
import { attributionZh } from '../modelAttribution'
import { diagnosticDaybreakZh } from '../diagnosticDaybreak'

export default {
  attribution: attributionZh,
  diagnosticDaybreak: diagnosticDaybreakZh,
  candyTests: candyTestsZh,
  ...landing,
  ...common,
  ...dashboard,
  ...channelMonitorV2,
  ...batchImage,
  admin,
  ...misc,
}
