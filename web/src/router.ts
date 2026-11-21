import { createRouter, createWebHistory } from 'vue-router'
import CampaignList from './views/CampaignList.vue'
import NewCampaign from './views/NewCampaign.vue'
import CampaignView from './views/CampaignView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: CampaignList },
    { path: '/campaigns/new', component: NewCampaign },
    { path: '/campaigns/:id', component: CampaignView, props: true },
  ],
})
