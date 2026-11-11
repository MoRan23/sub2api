package securityaudit

// Defaults are an editable starting point, never an implicit activation. Keep
// fresh slices so administrator edits cannot mutate another config snapshot.
func defaultCBRNTopicRules() []string {
	return []string{
		`regex:(?i)(?:大(?:规模|規模|型|量)(?:杀伤|殺傷|毁灭|毀滅)性?武器|(?:生物|生化|化学|化學|细菌|細菌|毒素|放射性)武器|核武器|核弹|核彈|原子弹|原子彈|氢弹|氫彈|脏弹|髒彈|毒气弹|毒氣彈|(?:^|[^a-z0-9_])(?:wmd|cbrn|weapons?[\s-]+of[\s-]+mass[\s-]+destruction|(?:biological|chemical|nuclear|radiological)[\s-]+weapons?|(?:atomic|hydrogen|dirty)[\s-]+bombs?|bioweapons?)(?:$|[^a-z0-9_]))`,
		`regex:(?i)(?:神经毒剂|神經毒劑|沙林|梭曼|塔崩|维埃克斯|維埃克斯|诺维乔克|諾維喬克|芥子气|芥子氣|炭疽杆菌|炭疽桿菌|肉毒毒素|蓖麻毒素|天花病毒|vx(?:毒剂|毒劑|神经毒剂|神經毒劑)|(?:^|[^a-z0-9_])(?:sarin|soman|tabun|novichok|mustard[\s-]+gas|nerve[\s-]+agents?|vx[\s-]+agent|anthrax|bacillus[\s-]+anthracis|botulinum[\s-]+toxin|ricin|variola[\s-]+virus)(?:$|[^a-z0-9_]))`,
	}
}

func defaultCBRNIntentRules() []string {
	return []string{
		`regex:(?i)(?:制造|製造|制备|製備|合成|提取|培养|培養|扩增|擴增|武器化|投放|散布|释放|釋放|采购|採購|购买|購買|获取|獲取|配方|配比|步骤|步驟|工艺|工藝|操作规程|操作規程|(?:^|[^a-z0-9_])(?:make|build|manufacture|synthesi[sz]e|extract|culture|weaponi[sz]e|disperse|release|buy|purchase|obtain|recipe|protocol|step[\s-]+by[\s-]+step)(?:$|[^a-z0-9_]))`,
	}
}
