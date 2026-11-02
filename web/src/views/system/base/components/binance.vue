<template>
  <a-spin :loading="loading" style="width: 100%">
    <a-card title="Binance Pay 个人收款配置">
      <a-alert style="margin-bottom: 16px">
        配置收款账户的币安用户 UID 和只读 API 密钥。保存后自动生效，无需重启。买家按收银台金额向该 UID 转入 USDT。
      </a-alert>
      <a-space style="margin-bottom: 16px">
        <a-tag :color="enabled ? 'green' : 'gray'">{{ enabled ? '已启用' : '未启用' }}</a-tag>
        <span>{{ source === 'database' ? '当前使用后台保存的配置' : '当前使用环境变量配置' }}</span>
      </a-space>
      <a-form :model="form" layout="vertical" class="base-setting-form" @submit="save">
        <a-form-item field="uid" label="币安用户 UID" extra="收款账户的用户 ID，不是 Pay ID；留空可停用收款。">
          <a-input v-model="form.uid" :disabled="form.clear_credentials" placeholder="例如：12345678" allow-clear />
        </a-form-item>
        <a-form-item field="api_key" label="API Key" :extra="keyConfigured ? '已配置；留空保留原值，输入新值可替换。' : '尚未配置，请输入只读 API Key。'">
          <a-input-password v-model="form.api_key" :disabled="form.clear_credentials" placeholder="输入 API Key" autocomplete="new-password" allow-clear />
        </a-form-item>
        <a-form-item field="api_secret" label="API Secret" :extra="secretConfigured ? '已配置；留空保留原值，输入新值可替换。' : '尚未配置，请输入 API Secret。'">
          <a-input-password v-model="form.api_secret" :disabled="form.clear_credentials" placeholder="输入 API Secret" autocomplete="new-password" allow-clear />
        </a-form-item>
        <a-form-item>
          <a-checkbox v-model="form.clear_credentials">清空已保存的 UID 和密钥，并停用 Binance Pay</a-checkbox>
        </a-form-item>
        <a-form-item extra="密钥不回显。仅开启读取权限，建议绑定服务器 IP。后台配置优先于环境变量。">
          <a-button type="primary" html-type="submit" :loading="saving" :disabled="!loaded">保存配置</a-button>
        </a-form-item>
      </a-form>
    </a-card>
  </a-spin>
</template>

<script setup lang="ts">
import { ref, onMounted } from "vue";
import { Message } from "@arco-design/web-vue";
import { getBinanceConfAPI, setBinanceConfAPI } from "@/api/modules/conf/index";
const loading = ref(false);
const loaded = ref(false);
const saving = ref(false);
const enabled = ref(false);
const source = ref("environment");
const keyConfigured = ref(false);
const secretConfigured = ref(false);
const form = ref({ uid: "", api_key: "", api_secret: "", clear_credentials: false });
async function load() {
  loading.value = true;
  try {
    const response = await getBinanceConfAPI();
    const data = response.data;
    form.value = { uid: data.uid || "", api_key: "", api_secret: "", clear_credentials: false };
    enabled.value = data.enabled;
    source.value = data.source;
    keyConfigured.value = data.api_key_configured;
    secretConfigured.value = data.api_secret_configured;
    loaded.value = true;
  } finally { loading.value = false; }
}
async function save() {
  const uid = form.value.uid.trim();
  if (!form.value.clear_credentials && uid && !/^[1-9][0-9]{3,19}$/.test(uid)) {
    Message.error("请输入有效的币安用户 UID（4–20 位数字）");
    return;
  }
  saving.value = true;
  try {
    await setBinanceConfAPI(form.value);
    Message.success("配置已保存，无需重启");
    await load();
  } finally { saving.value = false; }
}
onMounted(load);
</script>
