import landing from './landing'
import common from './common'
import dashboard from './dashboard'
import channelMonitorV2 from './channelMonitorV2'
import batchImage from './batchImage'
import admin from './admin'
import misc from './misc'
import { candyTestsEn } from '../candyTests'
import { attributionEn } from '../modelAttribution'

export default {
  attribution: attributionEn,
  candyTests: candyTestsEn,
  ...landing,
  ...common,
  ...dashboard,
  ...channelMonitorV2,
  ...batchImage,
  admin,
  ...misc,
}
