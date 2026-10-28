import adminAccessKeys from './adminAccessKeys'
import landing from './landing'
import common from './common'
import dashboard from './dashboard'
import channelMonitorV2 from './channelMonitorV2'
import batchImage from './batchImage'
import admin from './admin'
import misc from './misc'
import { candyTestsEn } from '../candyTests'
import { attributionEn } from '../modelAttribution'
import { diagnosticDaybreakEn } from '../diagnosticDaybreak'

export default {
  attribution: attributionEn,
  diagnosticDaybreak: diagnosticDaybreakEn,
  candyTests: candyTestsEn,
  adminAccessKeys,
  ...landing,
  ...common,
  ...dashboard,
  ...channelMonitorV2,
  ...batchImage,
  admin,
  ...misc,
}
